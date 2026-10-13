package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// ============================================================
// Состояния процесса
// ============================================================

type ProcessState string

const (
	StateAbsent  ProcessState = "Отсутствует"
	StateReady   ProcessState = "Готов"
	StateActive  ProcessState = "Активен"
	StateBlocked ProcessState = "Блокирован"
	StateInitIO  ProcessState = "Инициализация IO"
)

// Числовой код состояния для индикации PSW.
func (state ProcessState) Code() int {
	switch state {
	case StateAbsent:
		return 0
	case StateReady:
		return 1
	case StateActive:
		return 2
	case StateBlocked:
		return 3
	case StateInitIO:
		return 4
	default:
		return -1
	}
}

// Команда использует локальные адреса в памяти своего процесса.
type OperationCode string

const (
	OpCalculate OperationCode = "ADD"
	OpIO        OperationCode = "IO"
	OpExit      OperationCode = "EXIT"
)

type Command struct {
	Code          OperationCode
	Address1      int
	Address2      int
	ResultAddress int
	Direction     IODirection
	IOCount       int
}

// ============================================================
// PSW — Process Status Word
// ============================================================

type PSW struct {
	TaskID        int // Идентификатор задания
	Size          int // Размер задания в памяти
	PC            int // Текущий счетчик команд процесса
	TotalCommands int // Общее количество команд

	BasePriority int // Постоянный базовый приоритет
	DynPriority  int // Динамическая составляющая приоритета

	QuantumUsed int // Сколько команд выполнено в текущем кванте

	Memory      []int   // Память процесса, Size ячеек
	LastCommand Command // Последняя выполненная команда
	IORemaining int     // Оставшееся время операции ввода/вывода

	State                 ProcessState // Состояние процесса
	IOPercent, IODuration int          // Индивидуальные параметры задания из файла.
	HasTaskParameters     bool
}

// ============================================================
// CPU — Центральный процессор
// ============================================================

type CPU struct {
	ID            int
	ActiveProcID  int
	PC            int
	IsBusy        bool
	LastCommand   OperationCode // Тип последней команды на этом ЦП
	SystemProcID  int           // 0 либо идентификатор исполняемого системного процесса
	WaitingGlobal bool
	PendingAction string
}

// Отдельная запись системного процесса с собственным счётчиком и состоянием.
// Таблица ядра фиксирована и не занимает слоты пользовательских заданий.
type SystemProcess struct {
	ID           int
	ParentTaskID int
	ProcessSlot  int
	CPUID        int
	PC           int
	State        ProcessState
	CreatedTick  uint64
	FinishedTick uint64
}

// ============================================================
// SystemModel — модель операционной системы
// ============================================================

type SystemModel struct {
	mu sync.Mutex

	TotalMemory int
	FreeMemory  int

	Speed         float64
	Kvant         int
	IOProbability int // Процент IO среди команд до EXIT
	NInOut        int // Длительность IO в тактах после инициализации

	GlobalPC uint64

	IsRunning bool
	PauseLoad bool

	NextTaskID int

	CPUs []CPU

	ProcessTable [16]PSW
	PSW_Task     PSW // Подготовленное задание, ожидающее загрузки

	SystemProcesses                     [16]SystemProcess
	NextSystemID                        int
	CompletedSystemProcesses            int
	IODevices                           [16]IODevice
	Interrupts                          []Interrupt
	InterruptHistory                    []Interrupt
	RaisedInterrupts, HandledInterrupts int
	LastInterrupt                       string
	Transitions                         []StateTransition
	TGlob, GlobalOwner, GlobalRemaining int
	GlobalWaiters                       []int

	LastAction string

	Stats        ModelStatistics
	Metrics      [16]ProcessMetrics
	RNG          *rand.Rand
	Measurements []TaskMeasurement
	Config       *ParameterConfig
	ConfigPath   string
	Seed         int64
}

// ============================================================
// Создание системы
// ============================================================

func NewSystemModel() *SystemModel {
	return NewSystemModelWithSeed(time.Now().UnixNano())
}

func NewSystemModelWithSeed(seed int64) *SystemModel {
	return newSystemModel(seed, nil)
}

func newSystemModel(seed int64, config *ParameterConfig) *SystemModel {
	rng := rand.New(rand.NewSource(seed))

	sys := &SystemModel{
		TotalMemory: 1024 * 2,
		FreeMemory:  1024 * 2,

		Speed:         10.0,
		Kvant:         10,
		IOProbability: 20,
		NInOut:        3,

		GlobalPC: 0,

		IsRunning: true,
		PauseLoad: false,

		NextTaskID: 1,

		CPUs: make([]CPU, 3),

		LastAction: "Ожидание ввода...",

		RNG:  rng,
		Seed: seed,
	}
	if config != nil {
		copyConfig := *config
		sys.Config = &copyConfig
		p := GenerateSystemParameters(copyConfig, rng)
		sys.TotalMemory, sys.FreeMemory = p.Memory, p.Memory
		sys.Speed, sys.Kvant, sys.TGlob = float64(p.Speed), p.Quantum, p.Global
	}

	// Инициализация 3 процессоров
	for i := 0; i < len(sys.CPUs); i++ {
		sys.CPUs[i] = CPU{
			ID:           i + 1,
			ActiveProcID: -1,
			PC:           0,
			IsBusy:       false,
		}
	}

	// Инициализация таблицы процессов
	for i := range sys.ProcessTable {
		sys.Process(i, StateAbsent)
	}
	for i := range sys.SystemProcesses {
		sys.Process(len(sys.ProcessTable)+i, StateAbsent)
	}

	// Первое задание готовим заранее и заполняем память до отказа загрузки.
	sys.PSW_Task = sys.GenerateTask()
	for sys.LoadTask() {
	}

	return sys
}

// ============================================================
// Вывод справки
// ============================================================

func PrintHelp() {
	fmt.Println("=========================================================================")
	fmt.Println("  Модель ОС: Распределение времени ЦП")
	fmt.Println("  Вариант 5: 3 ЦП, относительные и динамические приоритеты")
	fmt.Println("=========================================================================")
	fmt.Println("Запуск:")
	fmt.Println("  go run .            — Запуск моделирования")
	fmt.Println("  go run . /?         — Вывод справки")
	fmt.Println("  go run . -t-globl=2 — Общие таблицы заняты на 2 такта")
	fmt.Println("  ЛР8: -params=params.txt -seed=25; файл читается один раз, seed по умолчанию 25")
	fmt.Println("  go run . -seed=25 -ticks=400 -results=results.csv — Пакетный прогон ЛР7")
	fmt.Println("  Параметры: -io=20 -n-inout=3 -kvant=10; длительности в модельных тактах")
	fmt.Println()
	fmt.Println("Директивы:")
	fmt.Println("  +                    — Увеличить скорость на 10%")
	fmt.Println("  -                    — Уменьшить скорость на 10%")
	fmt.Println("  q                    — Завершить моделирование")
	fmt.Println("=========================================================================")
}

// ============================================================
// Получение итогового приоритета
//
// Итоговый приоритет:
//     BasePriority + DynPriority
// ============================================================

func (sys *SystemModel) EffectivePriority(proc *PSW) int {
	return proc.BasePriority + proc.DynPriority
}

// ============================================================
// Генерация нового процесса
// ============================================================

func (sys *SystemModel) GenerateTask() PSW {
	if sys.Config != nil {
		c := sys.Config
		task := PSW{TaskID: sys.NextTaskID, State: StateAbsent, HasTaskParameters: true,
			Size:          GenerateParameterValue(c.Ranges[ParamTaskSize], sys.RNG),
			TotalCommands: GenerateParameterValue(c.Ranges[ParamTaskCommands], sys.RNG),
			IOPercent:     GenerateParameterValue(c.Ranges[ParamTaskIO], sys.RNG),
			IODuration:    GenerateParameterValue(c.Ranges[ParamTaskIODuration], sys.RNG),
			BasePriority:  GenerateParameterValue(c.Ranges[ParamTaskPriority], sys.RNG)}
		sys.NextTaskID++
		return task
	}
	// Базовый приоритет случайно от 1 до 10
	basePriority := 1 + sys.RNG.Intn(10)

	task := PSW{
		TaskID: sys.NextTaskID,

		Size: 128 + (sys.NextTaskID*32)%256,

		PC:            0,
		TotalCommands: 50 + (sys.NextTaskID*15)%100,

		// Базовый приоритет — постоянный
		BasePriority: basePriority,

		// Динамическая часть изначально отсутствует
		DynPriority: 0,

		QuantumUsed: 0,

		State: StateAbsent,
	}

	sys.NextTaskID++

	return task
}

// ============================================================
// Проверка памяти
// ============================================================

func (sys *SystemModel) HasFreeMemory(required int) bool {
	return sys.FreeMemory >= required
}

// ============================================================
// Загрузка нового процесса
// ============================================================

func (sys *SystemModel) LoadTask() bool {
	if sys.PauseLoad {
		return false
	}

	freeSlot := -1

	// Ищем свободный слот
	for i := 0; i < len(sys.ProcessTable); i++ {
		if sys.ProcessTable[i].State == StateAbsent {
			freeSlot = i
			break
		}
	}

	if freeSlot == -1 {
		return false
	}

	newTask := sys.PSW_Task

	// Проверка памяти
	if !sys.HasFreeMemory(newTask.Size) {
		return false
	}

	// Выделяем память
	sys.FreeMemory -= newTask.Size

	// Загружаем процесс
	newTask.Memory = make([]int, newTask.Size)
	for i := range newTask.Memory {
		newTask.Memory[i] = newTask.TaskID + i
	}
	sys.ProcessTable[freeSlot] = newTask
	sys.Process(freeSlot, StateReady)
	sys.RegisterTask(freeSlot)

	// Генерируем следующее задание только после успешной загрузки текущего.
	sys.PSW_Task = sys.GenerateTask()

	sys.LastAction = fmt.Sprintf(
		"Создан процесс #%d: Base=%d, Dyn=%d, Effective=%d",
		newTask.TaskID,
		newTask.BasePriority,
		newTask.DynPriority,
		newTask.BasePriority+newTask.DynPriority,
	)

	return true
}

// ============================================================
// Поиск READY-процесса с максимальным итоговым приоритетом
//
// Effective = BasePriority + DynPriority
// ============================================================

func (sys *SystemModel) GetNextProcessForCPU() int {
	bestIdx := -1
	bestPriority := -1

	for i := 0; i < len(sys.ProcessTable); i++ {
		proc := &sys.ProcessTable[i]

		if proc.State != StateReady {
			continue
		}

		effectivePriority := sys.EffectivePriority(proc)

		// Выбираем максимальный итоговый приоритет
		if bestIdx == -1 || effectivePriority > bestPriority {
			bestPriority = effectivePriority
			bestIdx = i
		}
	}

	return bestIdx
}

// Сохранение аппаратного счётчика команд в PSW активного процесса.
func (sys *SystemModel) SaveProcessState(cpu *CPU) {
	if cpu.ActiveProcID != -1 {
		sys.ProcessTable[cpu.ActiveProcID].PC = cpu.PC
	}
}

// Восстановление счётчика команд выбранного процесса на свободном ЦП.
func (sys *SystemModel) RestoreProcessState(cpu *CPU, procIdx int) {
	cpu.ActiveProcID = procIdx
	cpu.PC = sys.ProcessTable[procIdx].PC
	cpu.IsBusy = true
}

// ============================================================
// Назначение процесса на CPU
// ============================================================

func (sys *SystemModel) DispatchProcess(cpu *CPU) bool {
	if cpu.IsBusy {
		return false
	}

	// Ищем процесс с максимальным:
	// BasePriority + DynPriority
	bestIdx := sys.GetNextProcessForCPU()

	if bestIdx == -1 {
		cpu.WaitingGlobal = false
		sys.CancelGlobalWait(cpu.ID)
		return false
	}

	if !sys.TryGlobalAccess(cpu) {
		return false
	}
	proc := &sys.ProcessTable[bestIdx]

	// Переводим процесс в Active
	sys.Process(bestIdx, StateActive)

	// При повторном назначении сохраняем накопленное снижение приоритета.
	proc.QuantumUsed = 0

	sys.RestoreProcessState(cpu, bestIdx)
	sys.RecordDispatch(cpu.ID, proc.TaskID)

	sys.LastAction = fmt.Sprintf(
		"ЦП #%d: процесс #%d запущен (Base=%d, Dyn=%d, Effective=%d)",
		cpu.ID,
		proc.TaskID,
		proc.BasePriority,
		proc.DynPriority,
		sys.EffectivePriority(proc),
	)

	return true
}

// ============================================================
// Завершение процесса
// ============================================================

func (sys *SystemModel) FinishProcess(cpu *CPU) {
	if cpu.ActiveProcID == -1 {
		return
	}
	if !sys.TryGlobalAccess(cpu) {
		cpu.PendingAction = "EXIT"
		return
	}
	taskID := sys.ProcessTable[cpu.ActiveProcID].TaskID
	sys.RemoveProcess(cpu.ActiveProcID)
	sys.LastAction = fmt.Sprintf("Процесс #%d завершён на ЦП #%d", taskID, cpu.ID)
}

// ============================================================
// Окончание кванта
// ============================================================

func (sys *SystemModel) EndQuantum(cpu *CPU) {
	if cpu.ActiveProcID == -1 {
		return
	}
	if !sys.TryGlobalAccess(cpu) {
		cpu.PendingAction = "QUANTUM"
		return
	}
	proc := &sys.ProcessTable[cpu.ActiveProcID]
	sys.SaveProcessState(cpu)
	sys.UpdateProcessPriority(proc)
	sys.Process(cpu.ActiveProcID, StateReady)
	proc.QuantumUsed = 0
	cpu.ActiveProcID = -1
	cpu.PC = 0
	cpu.IsBusy = false
	cpu.PendingAction = ""
}

// ============================================================
// Динамический приоритет
//
// За каждый использованный квант приоритет процесса уменьшается на 1.
//
// Таким образом:
//
//     Effective = Base + Dyn
//
// BasePriority постоянен; DynPriority = -(PC / Kvant).
// ============================================================

func (sys *SystemModel) UpdateProcessPriority(proc *PSW) {
	proc.DynPriority = -(proc.PC / sys.Kvant)
}

// Назначение процессов выполняется до исполнения команд всех трёх ЦП.
// Поэтому один процесс не выполняет несколько команд за один системный такт.
func (sys *SystemModel) DispatchReadyProcesses() {
	for i := range sys.CPUs {
		if !sys.CPUs[i].IsBusy {
			sys.DispatchProcess(&sys.CPUs[i])
		}
	}
}

// Вместо чтения машинного кода генерируем очередную команду процесса.
// Последняя команда задания всегда EXIT и входит в TotalCommands.
func (sys *SystemModel) GenerateCommand(cpu *CPU) Command {
	proc := &sys.ProcessTable[cpu.ActiveProcID]
	if cpu.PC >= proc.TotalCommands-1 {
		return Command{Code: OpExit}
	}
	ioPercent, ioDuration := sys.TaskIOParameters(proc)
	if sys.RNG.Intn(100) < ioPercent {
		direction := IORead
		if sys.RNG.Intn(2) == 1 {
			direction = IOWrite
		}
		return Command{Code: OpIO, Direction: direction, IOCount: min(len(proc.Memory), max(1, ioDuration))}
	}
	size := len(proc.Memory)
	if size == 0 {
		return Command{Code: OpCalculate, Address1: -1}
	}
	return Command{
		Code:          OpCalculate,
		Address1:      (cpu.PC + 1) % size,
		Address2:      (cpu.PC + 2) % size,
		ResultAddress: cpu.PC % size,
	}
}

func (sys *SystemModel) DecodeCommand(command Command) (OperationCode, int, int, int) {
	return command.Code, command.Address1, command.Address2, command.ResultAddress
}

func (sys *SystemModel) ReadOperand(proc *PSW, address int) (int, error) {
	if address < 0 || address >= len(proc.Memory) {
		return 0, fmt.Errorf("адрес %d вне памяти процесса #%d", address, proc.TaskID)
	}
	return proc.Memory[address], nil
}

func (sys *SystemModel) WriteResult(proc *PSW, address, value int) error {
	if address < 0 || address >= len(proc.Memory) {
		return fmt.Errorf("адрес %d вне памяти процесса #%d", address, proc.TaskID)
	}
	proc.Memory[address] = value
	return nil
}

// Команда IO порождает системный процесс, который выполнится в этом же такте.
func (sys *SystemModel) InitializeIO(cpu *CPU) {
	if !sys.TryGlobalAccess(cpu) {
		cpu.PendingAction = "IO"
		return
	}
	cpu.PendingAction = ""
	sys.SaveProcessState(cpu)
	proc := &sys.ProcessTable[cpu.ActiveProcID]
	if len(proc.Memory) == 0 {
		sys.AbortProcess(cpu, fmt.Errorf("нет памяти для IO"))
		return
	}
	freeSlot := -1
	for i := range sys.SystemProcesses {
		if sys.SystemProcesses[i].State != StateReady && sys.SystemProcesses[i].State != StateActive {
			freeSlot = i
			break
		}
	}
	if freeSlot == -1 {
		sys.AbortProcess(cpu, fmt.Errorf("таблица системных процессов заполнена"))
		return
	}
	sys.NextSystemID++
	sys.SystemProcesses[freeSlot] = SystemProcess{
		ID: sys.NextSystemID, ParentTaskID: proc.TaskID,
		ProcessSlot: cpu.ActiveProcID, CPUID: cpu.ID,
		CreatedTick: sys.GlobalPC,
		State:       sys.SystemProcesses[freeSlot].State,
	}
	sys.Process(len(sys.ProcessTable)+freeSlot, StateReady)
	sys.Process(cpu.ActiveProcID, StateInitIO)
	proc.QuantumUsed = 0
	sys.LastAction = fmt.Sprintf("Создан системный процесс #%d: инициализация IO задания #%d", sys.NextSystemID, proc.TaskID)
	cpu.ActiveProcID = -1
	cpu.PC = 0
	cpu.IsBusy = false
}

// Системный процесс исполняется на освободившемся ЦП в такте обращения IO.
// Его собственная команда запускает устройство и блокирует родительский процесс.
func (sys *SystemModel) RunSystemProcesses() {
	for i := range sys.SystemProcesses {
		process := &sys.SystemProcesses[i]
		if process.State != StateReady {
			continue
		}
		var cpu *CPU
		for j := range sys.CPUs {
			if sys.CPUs[j].ID == process.CPUID && !sys.CPUs[j].IsBusy {
				cpu = &sys.CPUs[j]
				break
			}
		}
		if cpu == nil {
			continue
		}
		sys.Process(len(sys.ProcessTable)+i, StateActive)
		cpu.SystemProcID = process.ID
		cpu.IsBusy = true
		cpu.PC = process.PC + 1
		process.PC = cpu.PC
		parent := &sys.ProcessTable[process.ProcessSlot]
		if parent.TaskID == process.ParentTaskID && parent.State == StateInitIO {
			sys.StartIO(process.ProcessSlot, process.CPUID)
			_, duration := sys.TaskIOParameters(parent)
			sys.LastAction = fmt.Sprintf("Системный процесс #%d на ЦП #%d: IO задания #%d запущен, %d такта",
				process.ID, process.CPUID, parent.TaskID, duration)
		}
		sys.Process(len(sys.ProcessTable)+i, StateAbsent)
		process.FinishedTick = sys.GlobalPC
		sys.CompletedSystemProcesses++
		cpu.SystemProcID = 0
		cpu.IsBusy = false
		cpu.PC = 0
	}
}

// Процессоры IO и прерывания реализованы в interrupts.go.

func (sys *SystemModel) DoOperation(cpu *CPU, code OperationCode, operand1, operand2 int) (int, error) {
	switch code {
	case OpCalculate:
		return operand1 + operand2, nil
	case OpIO:
		sys.InitializeIO(cpu)
	case OpExit:
		sys.FinishProcess(cpu)

	default:
		return 0, fmt.Errorf("неизвестная команда %q", code)
	}
	return 0, nil
}

// Ошибочная команда или адрес приводят к выгрузке процесса, а не к panic.
func (sys *SystemModel) AbortProcess(cpu *CPU, err error) {
	taskID := sys.ProcessTable[cpu.ActiveProcID].TaskID
	sys.FinishProcess(cpu)
	sys.LastAction = fmt.Sprintf("Процесс #%d удалён по ошибке: %s", taskID, err)
}

func (sys *SystemModel) ExecuteCommand(cpu *CPU) {
	if !cpu.IsBusy || cpu.ActiveProcID == -1 {
		return
	}
	proc := &sys.ProcessTable[cpu.ActiveProcID]
	command := sys.GenerateCommand(cpu)
	// Длина команды равна одной ячейке: PC указывает на следующую команду.
	cpu.PC++
	sys.RecordCPUCommand(cpu)
	sys.SaveProcessState(cpu)
	sys.UpdateProcessPriority(proc)
	proc.QuantumUsed++
	proc.LastCommand = command
	cpu.LastCommand = command.Code
	code, address1, address2, resultAddress := sys.DecodeCommand(command)
	operand1, operand2 := 0, 0
	var err error
	if code == OpCalculate {
		operand1, err = sys.ReadOperand(proc, address1)
		if err == nil {
			operand2, err = sys.ReadOperand(proc, address2)
		}
	}
	if err != nil {
		sys.AbortProcess(cpu, err)
		return
	}
	result, err := sys.DoOperation(cpu, code, operand1, operand2)
	if err == nil && code == OpCalculate {
		err = sys.WriteResult(proc, resultAddress, result)
	}
	if err != nil {
		sys.AbortProcess(cpu, err)
		return
	}
	if code == OpCalculate {
		sys.LastAction = fmt.Sprintf("Процесс #%d: ADD M[%d] = %d + %d = %d",
			proc.TaskID, resultAddress, operand1, operand2, result)
	}
}

// ============================================================
// Один такт моделирования
// ============================================================

func (sys *SystemModel) StepSimulation() {
	sys.mu.Lock()
	defer sys.mu.Unlock()
	sys.GlobalPC++
	sys.StepGlobalAccess()
	// Начальное назначение — часть инициализации цикла.
	if sys.GlobalPC == 1 {
		sys.CheckSystemState()
	}
	sys.SampleStatistics()
	// 1. Одна пользовательская команда на каждом занятом ЦП.
	for i := range sys.CPUs {
		cpu := &sys.CPUs[i]
		if !cpu.IsBusy || cpu.ActiveProcID < 0 {
			continue
		}
		if cpu.WaitingGlobal && cpu.PendingAction == "" && !sys.TryGlobalAccess(cpu) {
			sys.Metrics[cpu.ActiveProcID].GlobalTicks++
			continue
		}
		switch cpu.PendingAction {
		case "EXIT":
			sys.FinishProcess(cpu)
			if cpu.WaitingGlobal {
				sys.Metrics[cpu.ActiveProcID].GlobalTicks++
			}
		case "IO":
			sys.InitializeIO(cpu)
			if cpu.WaitingGlobal {
				sys.Metrics[cpu.ActiveProcID].GlobalTicks++
			}
		case "QUANTUM": // ожидает обработки уже поставленного прерывания
			sys.Metrics[cpu.ActiveProcID].GlobalTicks++
		default:
			sys.ExecuteCommand(cpu)
		}
	}
	sys.RunSystemProcesses()
	// 2. Работа IO и обработка сигналов устройств.
	sys.StepIO()
	sys.HandleInterrupts()
	// 3. Прерывания по исчерпанию кванта.
	sys.CheckTimerInterrupts()
	sys.HandleInterrupts()
	// 4-5. Проверка состояния, загрузка и новые назначения.
	sys.CheckSystemState()
	// 6. Отрисовка вызывается главным циклом после окончания такта.
}

// ============================================================
// Изменение скорости
// ============================================================

func (sys *SystemModel) ChangeSpeed(factor float64) {
	sys.mu.Lock()
	defer sys.mu.Unlock()

	newSpeed := sys.Speed * factor

	if newSpeed < 0.1 {
		newSpeed = 0.1
	}

	if newSpeed > 1000.0 {
		newSpeed = 1000.0
	}

	sys.Speed = newSpeed
}

// ============================================================
// Получение скорости
// ============================================================

func (sys *SystemModel) GetSpeed() float64 {
	sys.mu.Lock()
	defer sys.mu.Unlock()

	return sys.Speed
}

// ============================================================
// Получение состояния системы
// ============================================================

func (sys *SystemModel) GetRunning() bool {
	sys.mu.Lock()
	defer sys.mu.Unlock()

	return sys.IsRunning
}

// ============================================================
// Включение cbreak/raw режима терминала
// ============================================================

func EnableRawMode() {
	if runtime.GOOS == "windows" {
		return
	}

	cb := exec.Command("stty", "cbreak", "-echo")
	cb.Stdin = os.Stdin

	_ = cb.Run()
}

// ============================================================
// Восстановление терминала
// ============================================================

func CleanupTerminal() {
	if runtime.GOOS != "windows" {
		sane := exec.Command("stty", "sane")
		sane.Stdin = os.Stdin

		_ = sane.Run()

		// Выход из альтернативного экрана
		fmt.Print("\033[?1049l")
	}

	// Показать курсор
	fmt.Print("\033[?25h")
}

// ============================================================
// Отрисовка интерфейса
// ============================================================

func (sys *SystemModel) HandleInput() {
	buf := make([]byte, 1)

	for {
		if !sys.GetRunning() {
			return
		}

		n, err := os.Stdin.Read(buf)

		if err != nil || n == 0 {
			continue
		}

		char := string(buf[0])

		switch char {

		case "+":

			sys.ChangeSpeed(1.10)

			sys.mu.Lock()
			sys.LastAction = fmt.Sprintf(
				"Нажато '+' -> Скорость увеличена до %.1f тактов/сек",
				sys.Speed,
			)
			sys.mu.Unlock()

		case "-":

			sys.ChangeSpeed(0.90)

			sys.mu.Lock()
			sys.LastAction = fmt.Sprintf(
				"Нажато '-' -> Скорость уменьшена до %.1f тактов/сек",
				sys.Speed,
			)
			sys.mu.Unlock()

		case "q", "Q":

			sys.mu.Lock()
			sys.IsRunning = false
			sys.mu.Unlock()

			return
		}
	}
}

// ============================================================
// MAIN
// ============================================================

func main() {

	// --------------------------------------------------------
	// Аргументы командной строки
	// --------------------------------------------------------

	if len(os.Args) > 1 {
		arg := os.Args[1]

		if arg == "/?" ||
			arg == "-help" ||
			arg == "--help" {

			PrintHelp()
			return
		}
	}

	// --------------------------------------------------------
	// Настройка терминала
	// --------------------------------------------------------

	options := flag.NewFlagSet("model", flag.ContinueOnError)
	sharedTicks := options.Int("t-globl", 0, "число тактов монопольного доступа к общим таблицам")
	seed := options.Int64("seed", 25, "начальное состояние генератора; одинаковое для повторяемости")
	paramsPath := options.String("params", "params.txt", "текстовый файл диапазонов параметров")
	ticks := options.Int("ticks", 0, "число тактов пакетного прогона; 0 = интерактивно")
	ioPercent := options.Int("io", 20, "процент команд IO до EXIT")
	ioDuration := options.Int("n-inout", 3, "длительность IO в тактах")
	quantum := options.Int("kvant", 10, "квант команд")
	results := options.String("results", "", "CSV завершённых заданий")
	if err := options.Parse(os.Args[1:]); err != nil {
		return
	}
	if *sharedTicks < 0 || *ticks < 0 || *ioPercent < 0 || *ioPercent > 100 || *ioDuration < 1 || *quantum < 1 {
		fmt.Fprintln(os.Stderr, "Требуется: t-globl,ticks >= 0; io = 0..100; n-inout,kvant >= 1")
		os.Exit(2)
	}
	config, err := ReadParameterFile(*paramsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	// Явно заданные ключи переопределяют диапазоны до начальной загрузки.
	options.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "t-globl":
			config.Ranges[ParamGlobal] = ParameterRange{*sharedTicks, *sharedTicks, 0}
		case "io":
			config.Ranges[ParamTaskIO] = ParameterRange{*ioPercent, *ioPercent, 0}
		case "n-inout":
			config.Ranges[ParamTaskIODuration] = ParameterRange{*ioDuration, *ioDuration, 0}
		case "kvant":
			config.Ranges[ParamQuantum] = ParameterRange{*quantum, *quantum, 0}
		}
	})
	model, err := NewConfiguredSystemModel(*seed, config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	model.ConfigPath = *paramsPath
	if *ticks > 0 {
		for i := 0; i < *ticks; i++ {
			model.StepSimulation()
		}
		fmt.Println(model.BuildScreen())
		if *results != "" {
			if err := model.ExportMeasurements(*results); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		return
	}
	EnableRawMode()

	if runtime.GOOS != "windows" {
		fmt.Print("\033[?1049h")
	}

	// Скрываем курсор
	fmt.Print("\033[?25l")

	// --------------------------------------------------------
	// Обработка Ctrl+C / SIGTERM
	// --------------------------------------------------------

	sigChan := make(chan os.Signal, 1)

	signal.Notify(
		sigChan,
		os.Interrupt,
		syscall.SIGTERM,
	)

	go func() {
		<-sigChan

		CleanupTerminal()

		fmt.Println("\nМоделирование завершено.")
		os.Exit(0)
	}()

	// --------------------------------------------------------
	// Создание модели
	// --------------------------------------------------------

	// Запрос размера окна поддерживается терминалами с xterm-совместимым VT.
	fmt.Printf("\033[8;%d;%dt\033[2J", ScreenHeight+1, ScreenWidth+1)

	// --------------------------------------------------------
	// Запуск обработки клавиатуры
	// --------------------------------------------------------

	go model.HandleInput()

	// --------------------------------------------------------
	// Главный цикл
	// --------------------------------------------------------

	for model.GetRunning() {

		model.StepSimulation()

		model.RenderUI()

		currentSpeed := model.GetSpeed()

		// Интервал между тактами
		delay := time.Duration(
			1000.0/currentSpeed,
		) * time.Millisecond

		time.Sleep(delay)
	}
	if *results != "" {
		if err := model.ExportMeasurements(*results); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}

	// --------------------------------------------------------
	// Завершение
	// --------------------------------------------------------

	CleanupTerminal()

	fmt.Println(
		"\nМоделирование завершено по команде оператора ('q').",
	)
}
