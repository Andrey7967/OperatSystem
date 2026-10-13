package main

import (
	"fmt"
	"strings"
)

// Все длительности измеряются в модельных тактах, независимо от скорости UI.
type ProcessMetrics struct {
	TaskID                                          int
	LoadedTick                                      uint64
	FirstRunTick                                    uint64
	Started                                         bool
	ReadyTicks, BlockedTicks, GlobalTicks, Executed uint64
}

type CPUStatistics struct {
	Commands        uint64
	LastCommandTick uint64
	BusyTicks       uint64
	History         []int
}

type ModelStatistics struct {
	Loaded, Completed, Failed                                                uint64
	InputSize, InputLength, InputPriority                                    uint64
	WaitSum, BlockedSum, GlobalSum, ExecutionSum, TurnaroundSum, ResponseSum uint64
	ResponseCount                                                            uint64
	ReadyQueueSum, BlockedQueueSum                                           uint64
	CPUs                                                                     [3]CPUStatistics
}

func (sys *SystemModel) RegisterTask(slot int) {
	p := &sys.ProcessTable[slot]
	sys.Metrics[slot] = ProcessMetrics{TaskID: p.TaskID, LoadedTick: sys.GlobalPC}
	sys.Stats.Loaded++
	sys.Stats.InputSize += uint64(p.Size)
	sys.Stats.InputLength += uint64(p.TotalCommands)
	sys.Stats.InputPriority += uint64(p.BasePriority)
}

// Состояние в начале интервала: ожидание до первого выполнения включено.
func (sys *SystemModel) SampleStatistics() {
	ready, blocked := sys.QueueLengths()
	sys.Stats.ReadyQueueSum += uint64(ready)
	sys.Stats.BlockedQueueSum += uint64(blocked)
	for slot := range sys.ProcessTable {
		p, m := &sys.ProcessTable[slot], &sys.Metrics[slot]
		if p.State == StateAbsent || m.TaskID != p.TaskID {
			continue
		}
		switch p.State {
		case StateReady:
			m.ReadyTicks++
		case StateBlocked, StateInitIO:
			m.BlockedTicks++
		}
	}
}

func (sys *SystemModel) RecordCPUCommand(cpu *CPU) {
	m := &sys.Metrics[cpu.ActiveProcID]
	p := &sys.ProcessTable[cpu.ActiveProcID]
	if m.TaskID == p.TaskID {
		m.Executed++
		if !m.Started {
			m.Started = true
			m.FirstRunTick = sys.GlobalPC
		}
	}
	c := &sys.Stats.CPUs[cpu.ID-1]
	c.Commands++
	if c.LastCommandTick != sys.GlobalPC {
		c.BusyTicks++
		c.LastCommandTick = sys.GlobalPC
	}
}

// История содержит назначения пользовательских процессов, включая повторы.
func (sys *SystemModel) RecordDispatch(cpuID, taskID int) {
	c := &sys.Stats.CPUs[cpuID-1]
	c.History = append(c.History, taskID)
	if len(c.History) > 16 {
		c.History = c.History[len(c.History)-16:]
	}
}

func (sys *SystemModel) CompleteTask(slot int) {
	m := &sys.Metrics[slot]
	if m.TaskID != sys.ProcessTable[slot].TaskID || m.TaskID == 0 {
		return
	}
	sys.Stats.Completed++
	if sys.ProcessTable[slot].LastCommand.Code != OpExit {
		sys.Stats.Failed++
	}
	sys.Stats.WaitSum += m.ReadyTicks
	sys.Stats.BlockedSum += m.BlockedTicks
	sys.Stats.GlobalSum += m.GlobalTicks
	sys.Stats.ExecutionSum += m.Executed
	sys.Stats.TurnaroundSum += sys.GlobalPC - m.LoadedTick
	if m.Started {
		sys.Stats.ResponseSum += m.FirstRunTick - m.LoadedTick
		sys.Stats.ResponseCount++
	}
	*m = ProcessMetrics{}
}

func average(sum, count uint64) float64 {
	if count == 0 {
		return 0
	}
	return float64(sum) / float64(count)
}

func (sys *SystemModel) QueueLengths() (ready, blocked int) {
	for _, p := range sys.ProcessTable {
		if p.State == StateReady {
			ready++
		}
		if p.State == StateBlocked || p.State == StateInitIO {
			blocked++
		}
	}
	return
}

const ScreenWidth = 160
const ScreenHeight = 42

// Одна строка всегда занимает фиксированную ширину, включая пустые ячейки.
func screenLine(text string) string {
	runes := []rune(strings.ReplaceAll(strings.ReplaceAll(text, "\n", " "), "\r", " "))
	if len(runes) > ScreenWidth {
		runes = runes[:ScreenWidth-1]
		runes = append(runes, '…')
	}
	return string(runes) + strings.Repeat(" ", ScreenWidth-len(runes))
}

func (sys *SystemModel) DisplayConstantInformation(lines []string) {
	lines[0] = "МОДЕЛЬ ОС | Лабораторная 6 | Вариант 25 -> 5 | 3 ЦП | Экран 160 x 42 (терминал от 161 x 43)"
	lines[1] = "Управление: [+] скорость +10%  [-] скорость -10%  [q] выход | В Windows ввод клавиши + Enter"
	lines[2] = "СИСТЕМА"
	lines[5] = "НОВОЕ ЗАДАНИЕ (подготовлено, ещё не загружено)"
	lines[7] = "ЦЕНТРАЛЬНЫЕ ПРОЦЕССОРЫ | Seq: последние 16 назначений, слева старые, справа новые"
	lines[11] = "ПРОЦЕССЫ | S: 0 отсутствует, 1 готов, 2 активен, 3 блокирован, 4 инициализация IO | M: размер выделенной памяти"
	lines[12] = "Слот Task S   PC/Всего     M Base  Dyn    P  Q IOrem CMD  A1 A2 AR Dir  Cnt | IO tr/cnt rem buf0 | загрузка отклик ожид IOож глоб вып"
	for slot := 0; slot < 16; slot++ {
		lines[13+slot] = fmt.Sprintf("[%02d] -", slot)
	}
	lines[29] = "СРЕДНИЕ ЗНАЧЕНИЯ | вход: все загруженные; выход: удалённые (завершение или ошибка); очередь: все такты"
	lines[34] = "Показатели в тактах: ожид=готовность, IOож=блокировка, глоб=ожидание таблиц активным ЦП, вып=исполненные команды включая EXIT."
	lines[35] = "Полезная загрузка ЦП = такты с пользовательской командой / все такты. Служебная инициализация IO в этот показатель не входит."
	lines[36] = "READ: буфер устройства -> память; WRITE: память -> буфер. Завершение IO переводит процесс в готовность через прерывание."
}

func (sys *SystemModel) DisplaySystemParameters(lines []string) {
	r, b := sys.QueueLengths()
	lines[3] = fmt.Sprintf("Такт=%d Скорость=%.1f Квант=%d IO=%d%% ДлительностьIO=%d Память=%d/%d Свободно=%d Готово=%d Блокировано=%d IRQ=%d", sys.GlobalPC, sys.Speed, sys.Kvant, sys.IOProbability, sys.NInOut, sys.TotalMemory-sys.FreeMemory, sys.TotalMemory, sys.FreeMemory, r, b, len(sys.Interrupts))
	lines[4] = fmt.Sprintf("T_Globl=%d ВладелецЦП=%d Осталось=%d ОчередьЦП=%v IRQ создано/принято=%d/%d SYS завершено=%d ЗагрузкаПауза=%t", sys.TGlob, sys.GlobalOwner, sys.GlobalRemaining, sys.GlobalWaiters, sys.RaisedInterrupts, sys.HandledInterrupts, sys.CompletedSystemProcesses, sys.PauseLoad)
	lines[37] = "Последний IRQ: " + sys.LastInterrupt
	lines[38] = "Событие: " + sys.LastAction
}

func (sys *SystemModel) DisplayNewTaskParameters(lines []string) {
	p := sys.PSW_Task
	lines[6] = fmt.Sprintf("TaskID=%d Размер=%d Команд=%d Base=%d Dyn=%d P=%d PC=%d КвантИсп=%d IOrem=%d S=%d Память ещё не выделена", p.TaskID, p.Size, p.TotalCommands, p.BasePriority, p.DynPriority, sys.EffectivePriority(&p), p.PC, p.QuantumUsed, p.IORemaining, p.State.Code())
}

func (sys *SystemModel) DisplayCPUParameters(lines []string) {
	for i, cpu := range sys.CPUs {
		c := sys.Stats.CPUs[i]
		task := 0
		if cpu.ActiveProcID >= 0 {
			task = sys.ProcessTable[cpu.ActiveProcID].TaskID
		}
		lines[8+i] = fmt.Sprintf("ЦП#%d Task=%d Slot=%d PC=%d Busy=%t SYS=%d Cmd=%s WaitGlobal=%t Pending=%s Cmds=%d Загрузка=%.2f%% | Seq=%v", cpu.ID, task, cpu.ActiveProcID, cpu.PC, cpu.IsBusy, cpu.SystemProcID, cpu.LastCommand, cpu.WaitingGlobal, cpu.PendingAction, c.Commands, 100*average(c.BusyTicks, sys.GlobalPC), c.History)
	}
}

func (sys *SystemModel) DisplayProcessParameters(lines []string) {
	for slot, p := range sys.ProcessTable {
		if p.State == StateAbsent {
			continue
		}
		m, dev, cmd := sys.Metrics[slot], sys.IODevices[slot], p.LastCommand
		buf := "-"
		if len(dev.Buffer) > 0 {
			buf = fmt.Sprint(dev.Buffer[0])
		}
		response := "-"
		if m.Started {
			response = fmt.Sprint(m.FirstRunTick - m.LoadedTick)
		}
		lines[13+slot] = fmt.Sprintf("[%02d] %4d %d %4d/%-5d %5d %4d %4d %4d %2d %5d %-4s %2d %2d %2d %-5s %3d | %2d/%-3d %3d %4s | %6d %6s %4d %4d %4d %4d", slot, p.TaskID, p.State.Code(), p.PC, p.TotalCommands, p.Size, p.BasePriority, p.DynPriority, sys.EffectivePriority(&p), p.QuantumUsed, p.IORemaining, cmd.Code, cmd.Address1, cmd.Address2, cmd.ResultAddress, cmd.Direction, cmd.IOCount, dev.Transferred, dev.Count, dev.Remaining, buf, m.LoadedTick, response, m.ReadyTicks, m.BlockedTicks, m.GlobalTicks, m.Executed)
	}
}

func (sys *SystemModel) DisplayExecutionSequence(lines []string) {
	// Истории закреплены в отдельных строках, независимо от длины параметров ЦП.
	for i := range sys.CPUs {
		lines[8+i] = strings.Split(lines[8+i], " | Seq=")[0]
		lines[39+i] = fmt.Sprintf("Последние 16 назначений ЦП#%d: %v", i+1, sys.Stats.CPUs[i].History)
	}
}

func (sys *SystemModel) DisplayAverageParameters(lines []string) {
	s := sys.Stats
	lines[30] = fmt.Sprintf("Вход N=%d: размер=%.2f длина=%.2f Base=%.2f | Выход N=%d (ошибки=%d)", s.Loaded, average(s.InputSize, s.Loaded), average(s.InputLength, s.Loaded), average(s.InputPriority, s.Loaded), s.Completed, s.Failed)
	lines[31] = fmt.Sprintf("Выход: ожид=%.2f IOож=%.2f глоб=%.2f вып=%.2f оборот=%.2f отклик=%.2f (N=%d)", average(s.WaitSum, s.Completed), average(s.BlockedSum, s.Completed), average(s.GlobalSum, s.Completed), average(s.ExecutionSum, s.Completed), average(s.TurnaroundSum, s.Completed), average(s.ResponseSum, s.ResponseCount), s.ResponseCount)
	lines[32] = fmt.Sprintf("Средняя очередь: готово=%.2f блокировано=%.2f | Производительность=%.4f успешных завершений/такт", average(s.ReadyQueueSum, sys.GlobalPC), average(s.BlockedQueueSum, sys.GlobalPC), average(s.Completed-s.Failed, sys.GlobalPC))
	lines[33] = "До появления наблюдений среднее равно 0. Отклик = первый запуск - загрузка; оборот = удаление - загрузка."
}

// Вызывается под mutex: все семь секций относятся к одному такту.
func (sys *SystemModel) BuildScreen() string {
	lines := make([]string, ScreenHeight)
	sys.DisplayConstantInformation(lines)
	sys.DisplaySystemParameters(lines)
	sys.DisplayNewTaskParameters(lines)
	sys.DisplayCPUParameters(lines)
	sys.DisplayProcessParameters(lines)
	sys.DisplayExecutionSequence(lines)
	sys.DisplayAverageParameters(lines)
	for i := range lines {
		lines[i] = screenLine(lines[i])
	}
	return strings.Join(lines, "\n")
}

func (sys *SystemModel) RenderUI() {
	sys.mu.Lock()
	defer sys.mu.Unlock()
	// Абсолютная позиция каждой строки исключает прокрутку и перенос строки.
	lines := strings.Split(sys.BuildScreen(), "\n")
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "\033[%d;1H%s\033[K", i+1, line)
	}
	fmt.Print(b.String())
}
