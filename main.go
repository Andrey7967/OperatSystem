package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Состояния процесса
type ProcessState string

const (
	StateAbsent  ProcessState = "Отсутствует"
	StateReady   ProcessState = "Готов"
	StateActive  ProcessState = "Активен"
	StateBlocked ProcessState = "Блокирован"
)

// PSW (Process Status Word) — Слово состояния процесса
type PSW struct {
	TaskID        int          // Идентификатор задания
	Size          int          // Размер задания в единицах памяти (V_task)
	PC            int          // Счетчик команд процесса (PCi)
	TotalCommands int          // Общая чистая длительность (N_cmnd)
	BasePriority  int          // Базовый (относительный) приоритет
	DynPriority   int          // Динамический приоритет
	State         ProcessState // Текущее состояние процесса
}

// CPU — Структура Центрального Процессора (Вариант 5: 3 ЦП)
type CPU struct {
	ID           int  // Номер процессора (1..3)
	ActiveProcID int  // Номер выполняемого процесса (-1 если простаивает)
	PC           int  // Аппаратный счетчик команд процессора
	IsBusy       bool // Флаг занятости
}

// SystemModel — Главная структура модели ОС
type SystemModel struct {
	mu sync.Mutex

	TotalMemory int     // V_озу: общий объем памяти
	FreeMemory  int     // Доступный объем памяти
	Speed       float64 // Speed: скорость работы (тактов/сек)
	Kvant       int     // Квант времени ЦП

	GlobalPC   uint64 // Общий аппаратный счетчик тактов работы модели
	IsRunning  bool   // Флаг работы основного цикла
	PauseLoad  bool   // Флаг приостановки загрузки новых заданий
	NextTaskID int    // Счетчик для генерации ID заданий

	CPUs         []CPU   // 3 центральных процессора
	ProcessTable [16]PSW // Таблица слов состояний процессов

	LastAction string // Сообщение о последней выполненной команде
}

func NewSystemModel() *SystemModel {
	sys := &SystemModel{
		TotalMemory: 1024,
		FreeMemory:  1024,
		Speed:       10.0,
		Kvant:       10,
		IsRunning:   true,
		NextTaskID:  1,
		CPUs:        make([]CPU, 3),
		LastAction:  "Ожидание ввода...",
	}

	for i := 0; i < len(sys.CPUs); i++ {
		sys.CPUs[i] = CPU{
			ID:           i + 1,
			ActiveProcID: -1,
			PC:           0,
			IsBusy:       false,
		}
	}

	for i := 0; i < len(sys.ProcessTable); i++ {
		sys.ProcessTable[i] = PSW{State: StateAbsent}
	}

	return sys
}

func PrintHelp() {
	fmt.Println("=========================================================================")
	fmt.Println("  Модель ОС: Распределение времени ЦП (Лабораторная работа №1)")
	fmt.Println("  Вариант 5: 3 ЦП, Относительные и динамические приоритеты")
	fmt.Println("=========================================================================")
	fmt.Println("Запуск:")
	fmt.Println("  go run main.go         — Запуск моделирования")
	fmt.Println("  go run main.go /?      — Вывод данной справки")
	fmt.Println()
	fmt.Println("Директивы управления во время работы:")
	fmt.Println("  +                      — Увеличить скорость моделирования на 10%")
	fmt.Println("  -                      — Уменьшить скорость моделирования на 10%")
	fmt.Println("  q                      — Завершить моделирование")
	fmt.Println("=========================================================================")
}

func (sys *SystemModel) GenerateTask() PSW {
	task := PSW{
		TaskID:        sys.NextTaskID,
		Size:          128 + (sys.NextTaskID*32)%256,
		PC:            0,
		TotalCommands: 50 + (sys.NextTaskID*15)%100,
		BasePriority:  5,
		DynPriority:   5,
		State:         StateReady,
	}
	sys.NextTaskID++
	return task
}

func (sys *SystemModel) HasFreeMemory(required int) bool {
	return sys.FreeMemory >= required
}

func (sys *SystemModel) LoadTask() bool {
	if sys.PauseLoad {
		return false
	}

	freeSlot := -1
	for i := 0; i < len(sys.ProcessTable); i++ {
		if sys.ProcessTable[i].State == StateAbsent {
			freeSlot = i
			break
		}
	}

	if freeSlot == -1 {
		return false
	}

	newTask := sys.GenerateTask()
	if !sys.HasFreeMemory(newTask.Size) {
		return false
	}

	sys.FreeMemory -= newTask.Size
	sys.ProcessTable[freeSlot] = newTask
	return true
}

func (sys *SystemModel) StepSimulation() {
	sys.mu.Lock()
	defer sys.mu.Unlock()

	sys.GlobalPC++

	if sys.GlobalPC == 1 || sys.GlobalPC%20 == 0 {
		sys.LoadTask()
	}

	for i := 0; i < len(sys.CPUs); i++ {
		cpu := &sys.CPUs[i]

		if !cpu.IsBusy {
			for pIdx := 0; pIdx < len(sys.ProcessTable); pIdx++ {
				proc := &sys.ProcessTable[pIdx]
				if proc.State == StateReady {
					proc.State = StateActive
					cpu.ActiveProcID = pIdx
					cpu.PC = proc.PC
					cpu.IsBusy = true
					break
				}
			}
		}

		if cpu.IsBusy && cpu.ActiveProcID != -1 {
			proc := &sys.ProcessTable[cpu.ActiveProcID]

			cpu.PC++
			proc.PC = cpu.PC

			if cpu.PC%10 == 0 && proc.DynPriority > 1 {
				proc.DynPriority--
			}

			if proc.PC >= proc.TotalCommands {
				proc.State = StateAbsent
				sys.FreeMemory += proc.Size
				cpu.IsBusy = false
				cpu.ActiveProcID = -1
			}
		}
	}
}

func (sys *SystemModel) ChangeSpeed(factor float64) {
	sys.mu.Lock()
	defer sys.mu.Unlock()

	newSpeed := sys.Speed * factor
	if newSpeed < 0.1 {
		newSpeed = 0.1
	} else if newSpeed > 1000.0 {
		newSpeed = 1000.0
	}
	sys.Speed = newSpeed
}

// EnableRawMode переводит терминал в cbreak-режим на UNIX, на Windows игнорируется
func EnableRawMode() {
	if runtime.GOOS == "windows" {
		return
	}
	cb := exec.Command("stty", "cbreak", "-echo")
	cb.Stdin = os.Stdin
	_ = cb.Run()
}

// CleanupTerminal восстанавливает настройки терминала
func CleanupTerminal() {
	if runtime.GOOS != "windows" {
		sane := exec.Command("stty", "sane")
		sane.Stdin = os.Stdin
		_ = sane.Run()
		fmt.Print("\033[?1049l")
	}
	fmt.Print("\033[?25h")
}

// RenderUI формирует кадр
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

	printlnLine("=========================================================================")
	printfLine("МОДЕЛЬ ОПЕРАЦИОННОЙ СИСТЕМЫ | Вариант 5 (3 ЦП, Динамические приоритеты)")
	printlnLine("=========================================================================")
	printfLine(" [Системный PC]: %-10d | [Скорость]: %-6.1f тактов/сек", sys.GlobalPC, sys.Speed)
	printfLine(" [Память ОЗУ]  : %d / %d (Свободно: %d)", sys.TotalMemory-sys.FreeMemory, sys.TotalMemory, sys.FreeMemory)
	printlnLine("-------------------------------------------------------------------------")

	printlnLine(" СОСТОЯНИЕ ЦЕНТРАЛЬНЫХ ПРОЦЕССОРОВ (ЦП):")
	for _, cpu := range sys.CPUs {
		status := "Ожидание"
		if cpu.IsBusy {
			status = fmt.Sprintf("Работа (Процесс #%d, PC=%d)", sys.ProcessTable[cpu.ActiveProcID].TaskID, cpu.PC)
		}
		printfLine("   ЦП #%d: %s", cpu.ID, status)
	}
	printlnLine("-------------------------------------------------------------------------")

	printlnLine(" ТАБЛИЦА ПРОЦЕССОВ (PSW):")
	printfLine("  %-4s %-8s %-10s %-8s %-10s %-12s", "Слот", "ID_Task", "Состояние", "PC/Total", "Размер", "Приоритет (Баз/Дин)")

	renderedRows := 0
	for i, proc := range sys.ProcessTable {
		if proc.State != StateAbsent {
			renderedRows++
			printfLine("  [%02d] %-8d %-10s %d/%-6d %-10d %d / %d",
				i, proc.TaskID, proc.State, proc.PC, proc.TotalCommands, proc.Size, proc.BasePriority, proc.DynPriority)
		}
	}

	for i := renderedRows; i < 10; i++ {
		b.WriteString("\033[K\n")
	}

	printlnLine("=========================================================================")
	printlnLine(" ДИРЕКТИВЫ ОПЕРАТОРА: [+] Скорость +10% | [-] Скорость -10% | [q] Выход")
	printlnLine("=========================================================================")
	printfLine(" [Статус команды]: %s", sys.LastAction)

	fmt.Print(b.String())
}

// HandleInput универсально считывает символы
func (sys *SystemModel) HandleInput() {
	buf := make([]byte, 1)
	for sys.IsRunning {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			continue
		}

		char := string(buf[0])

		switch char {
		case "+":
			sys.ChangeSpeed(1.10)
			sys.mu.Lock()
			sys.LastAction = fmt.Sprintf("Нажато '+' -> Скорость увеличена до %.1f тактов/сек", sys.Speed)
			sys.mu.Unlock()
		case "-":
			sys.ChangeSpeed(0.90)
			sys.mu.Lock()
			sys.LastAction = fmt.Sprintf("Нажато '-' -> Скорость уменьшена до %.1f тактов/сек", sys.Speed)
			sys.mu.Unlock()
		case "q", "Q":
			sys.mu.Lock()
			sys.IsRunning = false
			sys.mu.Unlock()

			CleanupTerminal()
			fmt.Println("\nМоделирование завершено по команде оператора ('q').")
			os.Exit(0)
		}
	}
}

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

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		CleanupTerminal()
		os.Exit(0)
	}()

	model := NewSystemModel()

	go model.HandleInput()

	for model.IsRunning {
		model.StepSimulation()
		model.RenderUI()

		model.mu.Lock()
		currentSpeed := model.Speed
		model.mu.Unlock()

		delayMs := time.Duration(1000.0/currentSpeed) * time.Millisecond
		time.Sleep(delayMs)
	}

	CleanupTerminal()
}
