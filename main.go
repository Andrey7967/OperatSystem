package main

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ============================================================
// 3. Тип для состояния процесса
// ============================================================

type ProcessState string

const (
	StateAbsent  ProcessState = "Отсутствует" // 0
	StateReady   ProcessState = "Готов"       // 1
	StateActive  ProcessState = "Активен"     // 2
	StateBlocked ProcessState = "Блокирован"  // 3
)

// Код состояния (для вывода)
func StateCode(s ProcessState) int {
	switch s {
	case StateAbsent:
		return 0
	case StateReady:
		return 1
	case StateActive:
		return 2
	case StateBlocked:
		return 3
	default:
		return -1
	}
}

// ============================================================
// 4. Слово состояния процесса (PSW)
// ============================================================

type PSW struct {
	TaskID        int          // Идентификатор задания
	Size          int          // Размер задания в памяти
	PC            int          // Счётчик команд
	TotalCommands int          // Общее количество команд
	BasePriority  int          // Базовый приоритет
	DynPriority   int          // Динамическая надбавка
	QuantumUsed   int          // Команд в текущем кванте
	State         ProcessState // Состояние процесса
}

// ============================================================
// 1. Параметры процессора (значения по умолчанию)
// ============================================================

type CPU struct {
	ID           int
	ActiveProcID int  // -1 = свободен
	PC           int
	IsBusy       bool
}

// ============================================================
// 2. Параметры системы (значения по умолчанию)
// ============================================================

type SystemModel struct {
	mu sync.Mutex

	// Параметры системы (по умолчанию)
	TotalMemory int     // Размер ОЗУ
	FreeMemory  int     // Свободная память
	Speed       float64 // Скорость моделирования (тактов/сек)
	Kvant       int     // Квант времени (команд)

	GlobalPC uint64 // Глобальный счётчик тактов

	IsRunning bool
	PauseLoad bool

	NextTaskID int

	// 1. Параметры процессоров
	CPUs []CPU

	// 5. Массив слов состояний процессов (PSW)
	ProcessTable [16]PSW

	// 6. Переменная для нового задания
	PSW_Task PSW

	LastAction string

	RNG *rand.Rand
}

// ============================================================
// Создание системы + инициализация (п. 7)
// ============================================================

func NewSystemModel() *SystemModel {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	sys := &SystemModel{
		// Параметры системы — значения по умолчанию
		TotalMemory: 1024,
		FreeMemory:  1024,
		Speed:       10.0,
		Kvant:       10,

		GlobalPC: 0,

		IsRunning: true,
		PauseLoad: false,

		NextTaskID: 1,

		// 3 ЦП (можно изменить под конкретный вариант)
		CPUs: make([]CPU, 3),

		LastAction: "Ожидание ввода...",

		RNG: rng,
	}

	// Инициализация процессоров
	for i := 0; i < len(sys.CPUs); i++ {
		sys.CPUs[i] = CPU{
			ID:           i + 1,
			ActiveProcID: -1,
			PC:           0,
			IsBusy:       false,
		}
	}

	// 7. Инициализация массива слов состояний процессов
	for i := range sys.ProcessTable {
		sys.ProcessTable[i] = PSW{
			State: StateAbsent,
		}
	}

	return sys
}

// ============================================================
// Справка
// ============================================================

func PrintHelp() {
	fmt.Println("=========================================================================")
	fmt.Println("  Модель ОС: Генерация и загрузка задач")
	fmt.Println("  Лабораторная работа 2 | Вариант 25")
	fmt.Println("=========================================================================")
	fmt.Println("Запуск:")
	fmt.Println("  go run main.go       — Запуск моделирования")
	fmt.Println("  go run main.go /?    — Вывод справки")
	fmt.Println()
	fmt.Println("Директивы:")
	fmt.Println("  +                    — Увеличить скорость на 10%")
	fmt.Println("  -                    — Уменьшить скорость на 10%")
	fmt.Println("  q                    — Завершить моделирование")
	fmt.Println("=========================================================================")
}

// ============================================================
// Итоговый приоритет
// ============================================================

func (sys *SystemModel) EffectivePriority(proc *PSW) int {
	return proc.BasePriority + proc.DynPriority
}

// ============================================================
// 8. Подпрограмма «Генерация нового задания»
// ============================================================

func (sys *SystemModel) GenerateTask() PSW {
	basePriority := 1 + sys.RNG.Intn(10) // 1..10

	task := PSW{
		TaskID:        sys.NextTaskID,
		Size:          64 + (sys.NextTaskID*16)%192, // 64..255
		PC:            0,
		TotalCommands: 40 + (sys.NextTaskID*10)%80, // 40..119
		BasePriority:  basePriority,
		DynPriority:   0,
		QuantumUsed:   0,
		State:         StateReady,
	}

	sys.NextTaskID++
	return task
}

// ============================================================
// 9. Подпрограмма «Проверка наличия доступной памяти»
// ============================================================

func (sys *SystemModel) HasFreeMemory(required int) bool {
	return sys.FreeMemory >= required
}

// ============================================================
// 10. Подпрограмма «Загрузка нового задания»
// ============================================================

func (sys *SystemModel) LoadTask() bool {
	if sys.PauseLoad {
		return false
	}

	// Ищем свободный слот в таблице PSW
	freeSlot := -1
	for i := 0; i < len(sys.ProcessTable); i++ {
		if sys.ProcessTable[i].State == StateAbsent {
			freeSlot = i
			break
		}
	}
	if freeSlot == -1 {
		sys.LastAction = "Нет свободных слотов в таблице процессов"
		return false
	}

	// 8. Генерируем новое задание
	sys.PSW_Task = sys.GenerateTask()

	// 9. Проверка памяти
	if !sys.HasFreeMemory(sys.PSW_Task.Size) {
		sys.LastAction = fmt.Sprintf(
			"Недостаточно памяти для задания #%d (нужно %d, свободно %d)",
			sys.PSW_Task.TaskID, sys.PSW_Task.Size, sys.FreeMemory,
		)
		return false
	}

	// Выделяем память
	sys.FreeMemory -= sys.PSW_Task.Size

	// Передаём в свободный слот таблицы
	sys.ProcessTable[freeSlot] = sys.PSW_Task

	sys.LastAction = fmt.Sprintf(
		"Загружено задание #%d в слот [%02d]: Size=%d, Base=%d",
		sys.PSW_Task.TaskID, freeSlot, sys.PSW_Task.Size, sys.PSW_Task.BasePriority,
	)
	return true
}

// ============================================================
// Поиск READY-процесса с максимальным приоритетом
// ============================================================

func (sys *SystemModel) FindHighestPriorityReadyProcess() int {
	bestIdx := -1
	bestPriority := -1

	for i := 0; i < len(sys.ProcessTable); i++ {
		proc := &sys.ProcessTable[i]
		if proc.State != StateReady {
			continue
		}
		ep := sys.EffectivePriority(proc)
		if ep > bestPriority {
			bestPriority = ep
			bestIdx = i
		}
	}
	return bestIdx
}

// ============================================================
// Назначение процесса на CPU
// ============================================================

func (sys *SystemModel) DispatchProcess(cpu *CPU) bool {
	if cpu.IsBusy {
		return false
	}

	bestIdx := sys.FindHighestPriorityReadyProcess()
	if bestIdx == -1 {
		return false
	}

	proc := &sys.ProcessTable[bestIdx]
	proc.State = StateActive
	proc.DynPriority = 0
	proc.QuantumUsed = 0

	cpu.ActiveProcID = bestIdx
	cpu.PC = proc.PC
	cpu.IsBusy = true

	sys.LastAction = fmt.Sprintf(
		"ЦП #%d: процесс #%d запущен (Effective=%d)",
		cpu.ID, proc.TaskID, sys.EffectivePriority(proc),
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

	proc := &sys.ProcessTable[cpu.ActiveProcID]
	taskID := proc.TaskID

	sys.FreeMemory += proc.Size
	sys.ProcessTable[cpu.ActiveProcID] = PSW{State: StateAbsent}

	cpu.ActiveProcID = -1
	cpu.PC = 0
	cpu.IsBusy = false

	sys.LastAction = fmt.Sprintf("Процесс #%d завершён на ЦП #%d", taskID, cpu.ID)
}

// ============================================================
// Окончание кванта
// ============================================================

func (sys *SystemModel) EndQuantum(cpu *CPU) {
	if cpu.ActiveProcID == -1 {
		return
	}

	proc := &sys.ProcessTable[cpu.ActiveProcID]
	if proc.PC >= proc.TotalCommands {
		sys.FinishProcess(cpu)
		return
	}

	taskID := proc.TaskID
	oldEff := sys.EffectivePriority(proc)

	proc.State = StateReady
	proc.QuantumUsed = 0

	cpu.ActiveProcID = -1
	cpu.PC = 0
	cpu.IsBusy = false

	sys.LastAction = fmt.Sprintf(
		"Процесс #%d: квант завершён (Effective=%d) → Ready",
		taskID, oldEff,
	)
}

// ============================================================
// Обновление динамических приоритетов
// ============================================================

func (sys *SystemModel) UpdateWaitingPriorities() {
	for i := 0; i < len(sys.ProcessTable); i++ {
		proc := &sys.ProcessTable[i]
		if proc.State == StateReady && proc.DynPriority < 10 {
			proc.DynPriority++
		}
	}
}

// ============================================================
// Один такт моделирования
// ============================================================

func (sys *SystemModel) StepSimulation() {
	sys.mu.Lock()
	defer sys.mu.Unlock()

	sys.GlobalPC++

	// Периодическая генерация новых заданий
	if sys.GlobalPC == 1 || sys.GlobalPC%15 == 0 {
		sys.LoadTask()
	}

	sys.UpdateWaitingPriorities()

	for i := 0; i < len(sys.CPUs); i++ {
		cpu := &sys.CPUs[i]

		if !cpu.IsBusy {
			sys.DispatchProcess(cpu)
		}

		if cpu.IsBusy && cpu.ActiveProcID != -1 {
			proc := &sys.ProcessTable[cpu.ActiveProcID]

			cpu.PC++
			proc.PC++
			proc.QuantumUsed++

			if proc.PC >= proc.TotalCommands {
				sys.FinishProcess(cpu)
				continue
			}
			if proc.QuantumUsed >= sys.Kvant {
				sys.EndQuantum(cpu)
			}
		}
	}
}

// ============================================================
// Изменение / получение скорости
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

func (sys *SystemModel) GetSpeed() float64 {
	sys.mu.Lock()
	defer sys.mu.Unlock()
	return sys.Speed
}

func (sys *SystemModel) GetRunning() bool {
	sys.mu.Lock()
	defer sys.mu.Unlock()
	return sys.IsRunning
}

// ============================================================
// Терминал
// ============================================================

func EnableRawMode() {
	if runtime.GOOS == "windows" {
		return
	}
	cb := exec.Command("stty", "cbreak", "-echo")
	cb.Stdin = os.Stdin
	_ = cb.Run()
}

func CleanupTerminal() {
	if runtime.GOOS != "windows" {
		sane := exec.Command("stty", "sane")
		sane.Stdin = os.Stdin
		_ = sane.Run()
		fmt.Print("\033[?1049l")
	}
	fmt.Print("\033[?25h")
}

// ============================================================
// 12. Индикация слов состояний процессов + формулы памяти
// ============================================================

func (sys *SystemModel) RenderUI() {
	sys.mu.Lock()
	defer sys.mu.Unlock()

	var b strings.Builder
	b.WriteString("\033[1;1H")

	printlnLine := func(s string) {
		b.WriteString(s)
		b.WriteString("\033[K\n")
	}
	printfLine := func(format string, a ...interface{}) {
		b.WriteString(fmt.Sprintf(format, a...))
		b.WriteString("\033[K\n")
	}

	occupied := sys.TotalMemory - sys.FreeMemory
	canLoad := sys.FreeMemory > 0 // упрощённо; реальная проверка — в HasFreeMemory

	printlnLine("=========================================================================")
	printfLine("МОДЕЛЬ ОС | Лаб.2 Генерация и загрузка | Вариант 25")
	printlnLine("=========================================================================")

	printfLine("[Системный PC]: %-10d | [Скорость]: %-6.1f тактов/сек",
		sys.GlobalPC, sys.Speed)

	// Формулы занятой и свободной памяти
	printfLine("[Память ОЗУ]  : занято = %d, свободно = %d, всего = %d",
		occupied, sys.FreeMemory, sys.TotalMemory)
	printfLine("               занято = TotalMemory - FreeMemory = %d - %d = %d",
		sys.TotalMemory, sys.FreeMemory, occupied)
	printfLine("[Можно загрузить новое задание]: %v  (FreeMemory > 0 и есть свободный слот)",
		canLoad && hasFreeSlot(sys))

	printlnLine("-------------------------------------------------------------------------")
	printlnLine("СОСТОЯНИЕ ЦЕНТРАЛЬНЫХ ПРОЦЕССОРОВ (ЦП):")

	for _, cpu := range sys.CPUs {
		status := "Ожидание"
		if cpu.IsBusy && cpu.ActiveProcID != -1 {
			proc := &sys.ProcessTable[cpu.ActiveProcID]
			status = fmt.Sprintf("Работа (Процесс #%d, PC=%d, Effective=%d)",
				proc.TaskID, cpu.PC, sys.EffectivePriority(proc))
		}
		printfLine("   ЦП #%d: %s", cpu.ID, status)
	}

	printlnLine("-------------------------------------------------------------------------")
	printlnLine("ТАБЛИЦА ПРОЦЕССОВ (PSW) — номер, ID, PC, код состояния:")

	printfLine(" %-5s %-8s %-12s %-10s %-8s %-6s %-6s %-6s",
		"Слот", "TaskID", "Состояние", "PC/Total", "Размер", "Base", "Dyn", "Код")
	printfLine(" %-5s %-8s %-12s %-10s %-8s %-6s %-6s %-6s",
		"", "", "", "", "", "", "", "ст.")

	rendered := 0
	for i, proc := range sys.ProcessTable {
		if proc.State == StateAbsent {
			continue
		}
		rendered++
		printfLine(" [%02d] %-8d %-12s %d/%-7d %-8d %-6d %-6d %-6d",
			i,
			proc.TaskID,
			proc.State,
			proc.PC,
			proc.TotalCommands,
			proc.Size,
			proc.BasePriority,
			proc.DynPriority,
			StateCode(proc.State),
		)
		printfLine("       Effective = Base + Dyn = %d + %d = %d",
			proc.BasePriority, proc.DynPriority, sys.EffectivePriority(&proc))
	}

	// Очистка лишних строк
	for i := rendered; i < 6; i++ {
		b.WriteString("\033[K\n")
		b.WriteString("\033[K\n")
	}

	printlnLine("=========================================================================")
	printlnLine("Приоритет: Effective = Base + Dyn | Квант = 10 команд")
	printlnLine("[+] Скорость +10% | [-] Скорость -10% | [q] Выход")
	printlnLine("=========================================================================")
	printfLine("[Статус]: %s", sys.LastAction)

	fmt.Print(b.String())
}

func hasFreeSlot(sys *SystemModel) bool {
	for _, p := range sys.ProcessTable {
		if p.State == StateAbsent {
			return true
		}
	}
	return false
}

// ============================================================
// Обработка клавиатуры
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
			sys.LastAction = fmt.Sprintf("Скорость увеличена до %.1f", sys.Speed)
			sys.mu.Unlock()
		case "-":
			sys.ChangeSpeed(0.90)
			sys.mu.Lock()
			sys.LastAction = fmt.Sprintf("Скорость уменьшена до %.1f", sys.Speed)
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
	if len(os.Args) > 1 {
		arg := os.Args[1]
		if arg == "/?" || arg == "-help" || arg == "--help" {
			PrintHelp()
			return
		}
	}

	EnableRawMode()
	if runtime.GOOS != "windows" {
		fmt.Print("\033[?1049h")
	}
	fmt.Print("\033[?25l")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		CleanupTerminal()
		fmt.Println("\nМоделирование завершено.")
		os.Exit(0)
	}()

	model := NewSystemModel()

	// ============================================================
	// 11. Цикл загрузки заданий для начала моделирования
	// ============================================================
	fmt.Println("Начальная загрузка заданий...")
	for i := 0; i < 4; i++ { // загружаем несколько заданий сразу
		if !model.LoadTask() {
			break
		}
	}

	go model.HandleInput()

	for model.GetRunning() {
		model.StepSimulation()
		model.RenderUI()

		currentSpeed := model.GetSpeed()
		delay := time.Duration(1000.0/currentSpeed) * time.Millisecond
		time.Sleep(delay)
	}

	CleanupTerminal()
	fmt.Println("\nМоделирование завершено по команде оператора ('q').")
}