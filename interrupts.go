package main

import "fmt"

type IODirection string

const (
	IORead  IODirection = "READ"
	IOWrite IODirection = "WRITE"
)

// Устройство имеет собственные буфер, счётчик времени и позицию передачи.
type IODevice struct {
	ID, ProcessSlot, TaskID                int
	CPUID                                  int
	Direction                              IODirection
	Address, Count, Transferred, Remaining int
	Buffer                                 []int
	Active, InterruptPending               bool
	StartedTick                            uint64
}

type InterruptKind string

const (
	InterruptIO    InterruptKind = "IO_COMPLETE"
	InterruptTimer InterruptKind = "QUANTUM_END"
	InterruptFault InterruptKind = "IO_ERROR"
)

type Interrupt struct {
	Kind                                 InterruptKind
	ProcessSlot, TaskID, CPUID, DeviceID int
	Tick                                 uint64
	Message                              string
}

type StateTransition struct {
	Tick     uint64
	ID       int
	System   bool
	From, To ProcessState
}

// Единственный регулировщик состояний: 0..15 — пользовательские PSW,
// 16..31 — отдельные записи системных процессов.
func (sys *SystemModel) Process(slot int, next ProcessState) {
	var state *ProcessState
	id := 0
	system := slot >= len(sys.ProcessTable)
	if system {
		p := &sys.SystemProcesses[slot-len(sys.ProcessTable)]
		state, id = &p.State, p.ID
	} else {
		p := &sys.ProcessTable[slot]
		state, id = &p.State, p.TaskID
	}
	if *state == next {
		return
	}
	change := StateTransition{Tick: sys.GlobalPC, ID: id, System: system, From: *state, To: next}
	*state = next
	sys.Transitions = append(sys.Transitions, change)
	if len(sys.Transitions) > 32 {
		sys.Transitions = sys.Transitions[len(sys.Transitions)-32:]
	}
}

// TGlob=0 означает атомарное обращение в текущем такте. При TGlob>0
// владелец удерживает общие таблицы заданное число тактов; остальные ЦП ждут.
func (sys *SystemModel) TryGlobalAccess(cpu *CPU) bool {
	blocked := sys.GlobalRemaining > 0 && sys.GlobalOwner != cpu.ID
	if sys.TGlob > 0 && sys.GlobalRemaining == 0 && len(sys.GlobalWaiters) > 0 && sys.GlobalWaiters[0] != cpu.ID {
		blocked = true
	}
	if blocked {
		cpu.WaitingGlobal = true
		found := false
		for _, id := range sys.GlobalWaiters {
			if id == cpu.ID {
				found = true
				break
			}
		}
		if !found {
			sys.GlobalWaiters = append(sys.GlobalWaiters, cpu.ID)
		}
		return false
	}
	cpu.WaitingGlobal = false
	sys.CancelGlobalWait(cpu.ID)
	if sys.TGlob > 0 && sys.GlobalRemaining == 0 {
		sys.GlobalOwner = cpu.ID
		sys.GlobalRemaining = sys.TGlob
	}
	return true
}

func (sys *SystemModel) CancelGlobalWait(cpuID int) {
	filtered := sys.GlobalWaiters[:0]
	for _, id := range sys.GlobalWaiters {
		if id != cpuID {
			filtered = append(filtered, id)
		}
	}
	sys.GlobalWaiters = filtered
}

func (sys *SystemModel) StepGlobalAccess() {
	if sys.GlobalRemaining > 0 {
		sys.GlobalRemaining--
		if sys.GlobalRemaining == 0 {
			sys.GlobalOwner = 0
		}
	}
}

func (sys *SystemModel) StartIO(slot, cpuID int) {
	p := &sys.ProcessTable[slot]
	command := p.LastCommand
	count := command.IOCount
	if count <= 0 || count > len(p.Memory) {
		count = min(len(p.Memory), max(1, sys.NInOut))
	}
	direction := command.Direction
	if direction != IORead && direction != IOWrite {
		direction = IORead
	}
	duration := max(1, sys.NInOut)
	device := IODevice{ID: slot + 1, ProcessSlot: slot, TaskID: p.TaskID, CPUID: cpuID,
		Direction: direction, Address: command.ResultAddress, Count: count,
		Remaining: duration, Buffer: make([]int, count), Active: true, StartedTick: sys.GlobalPC}
	if direction == IORead {
		// Детерминированный поток данных моделируемого устройства ввода.
		for i := range device.Buffer {
			device.Buffer[i] = p.TaskID*100 + i
		}
	}
	sys.IODevices[slot] = device
	p.IORemaining = duration
	sys.Process(slot, StateBlocked)
}

// Один такт всех устройств IO. Запущенное в текущем такте устройство
// начинает отсчёт со следующего такта, без отдельной задержки инициализации.
func (sys *SystemModel) StepIO() {
	for i := range sys.IODevices {
		device := &sys.IODevices[i]
		if !device.Active || device.StartedTick >= sys.GlobalPC {
			continue
		}
		parent := &sys.ProcessTable[device.ProcessSlot]
		if parent.TaskID != device.TaskID || parent.State != StateBlocked {
			device.Active = false
			continue
		}
		// Передаём все слова к моменту окончания даже при Count > Remaining.
		words := (device.Count - device.Transferred + device.Remaining - 1) / device.Remaining
		for word := 0; word < words; word++ {
			offset := device.Transferred
			address := device.Address + offset
			var err error
			if device.Direction == IORead {
				err = sys.WriteResult(parent, address, device.Buffer[offset])
			} else {
				device.Buffer[offset], err = sys.ReadOperand(parent, address)
			}
			if err != nil {
				device.Active = false
				device.InterruptPending = true
				sys.RaiseInterrupt(Interrupt{Kind: InterruptFault, ProcessSlot: device.ProcessSlot,
					TaskID: device.TaskID, DeviceID: device.ID, Message: err.Error()})
				break
			}
			device.Transferred++
		}
		if !device.Active {
			continue
		}
		device.Remaining--
		// Однопрограммный эталон: только работа устройства, без ожидания IRQ.
		m := &sys.Metrics[device.ProcessSlot]
		if m.TaskID == device.TaskID {
			m.MonoTicks++
			m.IOTicks++
		}
		parent.IORemaining = device.Remaining
		if device.Remaining == 0 {
			device.Active = false
			device.InterruptPending = true
			sys.RaiseInterrupt(Interrupt{Kind: InterruptIO, ProcessSlot: device.ProcessSlot,
				TaskID: device.TaskID, DeviceID: device.ID, CPUID: device.CPUID})
		}
	}
}

func (sys *SystemModel) RaiseInterrupt(signal Interrupt) {
	signal.Tick = sys.GlobalPC
	sys.Interrupts = append(sys.Interrupts, signal)
	sys.InterruptHistory = append(sys.InterruptHistory, signal)
	if len(sys.InterruptHistory) > 32 {
		sys.InterruptHistory = sys.InterruptHistory[len(sys.InterruptHistory)-32:]
	}
	sys.RaisedInterrupts++
}

// Устройство только посылает сигнал. Состояние меняет обработчик через Process.
func (sys *SystemModel) HandleInterrupts() {
	pending := sys.Interrupts
	sys.Interrupts = nil
	for _, signal := range pending {
		if signal.ProcessSlot < 0 || signal.ProcessSlot >= len(sys.ProcessTable) {
			continue
		}
		p := &sys.ProcessTable[signal.ProcessSlot]
		if p.TaskID != signal.TaskID || p.State == StateAbsent {
			continue
		}
		switch signal.Kind {
		case InterruptIO:
			device := &sys.IODevices[signal.ProcessSlot]
			if p.State != StateBlocked || !device.InterruptPending || device.TaskID != signal.TaskID || device.ID != signal.DeviceID {
				continue
			}
			coordinator := &sys.CPUs[0]
			for i := range sys.CPUs {
				if sys.CPUs[i].ID == signal.CPUID {
					coordinator = &sys.CPUs[i]
					break
				}
			}
			if !sys.TryGlobalAccess(coordinator) {
				sys.Interrupts = append(sys.Interrupts, signal)
				continue
			}
			// Обработчик IO не выбирает новый процесс и не меняет таблицу памяти.
			device.InterruptPending = false
			sys.Process(signal.ProcessSlot, StateReady)
		case InterruptTimer:
			cpu := sys.CPUForProcess(signal.ProcessSlot)
			if cpu == nil || p.State != StateActive || cpu.ID != signal.CPUID || cpu.PendingAction != "QUANTUM" || p.QuantumUsed < sys.Kvant {
				continue
			}
			sys.EndQuantum(cpu)
			if cpu.WaitingGlobal {
				sys.Interrupts = append(sys.Interrupts, signal)
				continue
			}
		case InterruptFault:
			device := &sys.IODevices[signal.ProcessSlot]
			if !device.InterruptPending || device.TaskID != signal.TaskID {
				continue
			}
			coordinator := &sys.CPUs[0]
			if !sys.TryGlobalAccess(coordinator) {
				sys.Interrupts = append(sys.Interrupts, signal)
				continue
			}
			sys.RemoveProcess(signal.ProcessSlot)
		default:
			continue
		}
		sys.HandledInterrupts++
		sys.LastInterrupt = fmt.Sprintf("%s: задача #%d, IO #%d, такт %d", signal.Kind, signal.TaskID, signal.DeviceID, signal.Tick)
		sys.LastAction = sys.LastInterrupt
	}
}

func (sys *SystemModel) CPUForProcess(slot int) *CPU {
	for i := range sys.CPUs {
		if sys.CPUs[i].ActiveProcID == slot {
			return &sys.CPUs[i]
		}
	}
	return nil
}

func (sys *SystemModel) RemoveProcess(slot int) {
	p := &sys.ProcessTable[slot]
	if p.State == StateAbsent {
		return
	}
	sys.CompleteTask(slot)
	sys.FreeMemory += p.Size
	sys.Process(slot, StateAbsent)
	sys.ProcessTable[slot] = PSW{State: p.State}
	sys.IODevices[slot].Active = false
	sys.IODevices[slot].InterruptPending = false
	if cpu := sys.CPUForProcess(slot); cpu != nil {
		cpu.ActiveProcID = -1
		cpu.PC = 0
		cpu.IsBusy = false
		cpu.WaitingGlobal = false
		cpu.PendingAction = ""
	}
}

func (sys *SystemModel) CheckTimerInterrupts() {
	for i := range sys.CPUs {
		cpu := &sys.CPUs[i]
		if !cpu.IsBusy || cpu.ActiveProcID < 0 || cpu.PendingAction != "" {
			continue
		}
		p := &sys.ProcessTable[cpu.ActiveProcID]
		if p.State == StateActive && p.QuantumUsed >= sys.Kvant {
			cpu.PendingAction = "QUANTUM"
			sys.RaiseInterrupt(Interrupt{Kind: InterruptTimer, ProcessSlot: cpu.ActiveProcID, TaskID: p.TaskID, CPUID: cpu.ID})
		}
	}
}

// Назначает только свободные ЦП. Активное задание не вытесняется новым
// приоритетным заданием до конца кванта, IO или завершения.
func (sys *SystemModel) CheckSystemState() {
	for i := range sys.CPUs {
		cpu := &sys.CPUs[i]
		if cpu.IsBusy {
			continue
		}
		if sys.GetNextProcessForCPU() < 0 && !sys.PauseLoad {
			if sys.TryGlobalAccess(cpu) {
				for sys.LoadTask() {
				}
			}
		}
		sys.DispatchProcess(cpu)
	}
}
