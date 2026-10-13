package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// ExecuteDirective сериализует команды оператора с тактами модели.
func (sys *SystemModel) ExecuteDirective(text string) error {
	sys.mu.Lock()
	defer sys.mu.Unlock()
	message, err := sys.executeDirectiveLocked(strings.Fields(text))
	if err != nil {
		sys.LastDirective = "Ошибка: " + err.Error()
	} else {
		sys.LastDirective = message
	}
	return err
}

func number(s string, minValue, maxValue int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < minValue || n > maxValue {
		return 0, fmt.Errorf("число %q вне %d..%d", s, minValue, maxValue)
	}
	return n, nil
}

func (sys *SystemModel) executeDirectiveLocked(f []string) (string, error) {
	if len(f) == 0 {
		return "", nil
	}
	cmd := strings.ToLower(f[0])
	arity := func(n int) error {
		if len(f) != n {
			return fmt.Errorf("%s: неверное число аргументов", cmd)
		}
		return nil
	}
	switch cmd {
	case "q", "quit", "+", "-", "pause-load", "resume-load", "load", "finish", "help":
		if err := arity(1); err != nil {
			return "", err
		}
	case "speed", "memory", "suspend", "resume", "kill":
		if err := arity(2); err != nil {
			return "", err
		}
	case "task":
		if len(f) < 2 {
			return "", fmt.Errorf("task size=N commands=N io=N duration=N priority=N")
		}
	default:
		return "", fmt.Errorf("неизвестная директива %q; введите help", f[0])
	}
	switch cmd {
	case "q", "quit":
		sys.IsRunning = false
		return "Питание выключено оператором", nil
	case "+":
		sys.changeSpeedLocked(1.1)
		return fmt.Sprintf("Скорость %.2f", sys.Speed), nil
	case "-":
		sys.changeSpeedLocked(.9)
		return fmt.Sprintf("Скорость %.2f", sys.Speed), nil
	case "speed":
		n, err := strconv.ParseFloat(f[1], 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < .1 || n > 1000 {
			return "", fmt.Errorf("speed: требуется 0.1..1000 тактов/с")
		}
		sys.Speed = n
		return fmt.Sprintf("Скорость %.2f тактов/с", n), nil
	case "task":
		// Проверяется копия: ошибка в любом поле не меняет подготовленное задание.
		task := sys.PSW_Task
		if !task.HasTaskParameters {
			task.IOPercent, task.IODuration = sys.TaskIOParameters(&task)
		}
		seen := map[string]bool{}
		for _, arg := range f[1:] {
			pair := strings.SplitN(arg, "=", 2)
			if len(pair) != 2 {
				return "", fmt.Errorf("task: ожидается key=value")
			}
			key := strings.ToLower(pair[0])
			if seen[key] {
				return "", fmt.Errorf("task: повтор поля %s", key)
			}
			seen[key] = true
			low, high := 1, 65535
			switch key {
			case "io":
				low, high = 0, 100
			case "priority":
				low = 0
			case "size", "commands", "duration":
			default:
				return "", fmt.Errorf("task: неизвестное поле %s", key)
			}
			n, err := number(pair[1], low, high)
			if err != nil {
				return "", err
			}
			switch key {
			case "size":
				task.Size = n
			case "commands":
				task.TotalCommands = n
			case "io":
				task.IOPercent = n
			case "duration":
				task.IODuration = n
			case "priority":
				task.BasePriority = n
			}
		}
		task.HasTaskParameters = true
		sys.PSW_Task = task
		return fmt.Sprintf("Изменено новое задание #%d", task.TaskID), nil
	case "pause-load":
		sys.PauseLoad = true
		return "Автоматическая загрузка приостановлена", nil
	case "resume-load":
		if sys.DrainRequested {
			return "", fmt.Errorf("выполняется finish; новые задания запрещены")
		}
		sys.PauseLoad = false
		return "Автоматическая загрузка возобновлена", nil
	case "load":
		if sys.DrainRequested {
			return "", fmt.Errorf("выполняется finish; новые задания запрещены")
		}
		id := sys.PSW_Task.TaskID
		if !sys.loadPreparedTask() {
			return "", fmt.Errorf("нет свободного слота или памяти для задания #%d", id)
		}
		return fmt.Sprintf("Вручную загружено задание #%d", id), nil
	case "memory":
		n, err := number(f[1], 1, 65535)
		if err != nil {
			return "", err
		}
		used := sys.TotalMemory - sys.FreeMemory
		if n < used {
			return "", fmt.Errorf("занято %d ячеек; память нельзя уменьшить до %d", used, n)
		}
		sys.TotalMemory = n
		sys.FreeMemory = n - used
		return fmt.Sprintf("Память %d, свободно %d", n, sys.FreeMemory), nil
	case "suspend", "resume", "kill":
		id, err := number(f[1], 1, int(^uint(0)>>1))
		if err != nil {
			return "", err
		}
		slot := -1
		for i, p := range sys.ProcessTable {
			if p.State != StateAbsent && p.TaskID == id {
				slot = i
				break
			}
		}
		if slot < 0 {
			return "", fmt.Errorf("загруженное задание #%d не найдено", id)
		}
		p := &sys.ProcessTable[slot]
		switch cmd {
		case "suspend":
			if sys.DrainRequested {
				return "", fmt.Errorf("при finish приостановка запрещена")
			}
			p.Suspended = true
			if cpu := sys.CPUForProcess(slot); cpu != nil {
				sys.SaveProcessState(cpu)
				if cpu.PendingAction == "IO" || cpu.PendingAction == "EXIT" {
					p.DeferredAction = cpu.PendingAction
				}
				sys.CancelGlobalWait(cpu.ID)
				cpu.ActiveProcID = -1
				cpu.PC = 0
				cpu.IsBusy = false
				cpu.WaitingGlobal = false
				cpu.PendingAction = ""
				sys.Process(slot, StateReady)
			}
			sys.removeTaskInterrupts(slot, id, true)
		case "resume":
			p.Suspended = false
		case "kill":
			p.LastCommand.Code = "KILL"
			if cpu := sys.CPUForProcess(slot); cpu != nil {
				sys.CancelGlobalWait(cpu.ID)
			}
			for i, sp := range sys.SystemProcesses {
				if sp.State != StateAbsent && sp.ParentTaskID == id {
					sys.Process(len(sys.ProcessTable)+i, StateAbsent)
					sys.SystemProcesses[i].FinishedTick = sys.GlobalPC
				}
			}
			sys.removeTaskInterrupts(slot, id, false)
			sys.RemoveProcess(slot)
			sys.CheckDrainCompletion()
		}
		return fmt.Sprintf("%s: задание #%d", cmd, id), nil
	case "finish":
		sys.PauseLoad = true
		sys.DrainRequested = true
		// Возобновляем удержанные задания, иначе завершение могло бы ждать бесконечно.
		for i := range sys.ProcessTable {
			sys.ProcessTable[i].Suspended = false
		}
		sys.CheckDrainCompletion()
		if !sys.IsRunning {
			return "Все загруженные задания завершены; питание выключено", nil
		}
		return "Дождаться загруженных заданий и выключить питание", nil
	case "help":
		return "task size/commands/io/duration/priority=N; pause-load; resume-load; load; suspend/resume/kill TaskID; memory N; speed N; finish; q", nil
	}
	return "", nil
}

func (sys *SystemModel) removeTaskInterrupts(slot, id int, timerOnly bool) {
	signals := sys.Interrupts[:0]
	for _, s := range sys.Interrupts {
		if s.ProcessSlot == slot && s.TaskID == id && (!timerOnly || s.Kind == InterruptTimer) {
			continue
		}
		signals = append(signals, s)
	}
	sys.Interrupts = signals
}

// AllTasksCompleted проверяет таблицу резидентных заданий; PSW_Task не загружено.
// Внутренние функции модели вызываются при удерживаемом sys.mu.
func (sys *SystemModel) AllTasksCompleted() bool {
	for _, p := range sys.ProcessTable {
		if p.State != StateAbsent {
			return false
		}
	}
	return true
}
func (sys *SystemModel) CheckDrainCompletion() {
	if sys.DrainRequested && sys.AllTasksCompleted() {
		sys.IsRunning = false
		sys.LastDirective = "Все загруженные задания завершены; питание выключено"
	}
}

// Ввод не держит mutex во время ожидания клавиатуры. Служебные клавиши
// +, -, q срабатывают сразу, только когда строка директивы пуста.
func (sys *SystemModel) HandleInput() {
	reader := bufio.NewReader(os.Stdin)
	for sys.GetRunning() {
		r, _, err := reader.ReadRune()
		if err != nil {
			_ = sys.ExecuteDirective("q")
			return
		}
		sys.mu.Lock()
		switch {
		case r == '\r' || r == '\n':
			text := sys.InputLine
			sys.InputLine = ""
			sys.mu.Unlock()
			if strings.TrimSpace(text) != "" {
				_ = sys.ExecuteDirective(text)
			}
			continue
		case r == 127 || r == '\b':
			line := []rune(sys.InputLine)
			if len(line) > 0 {
				sys.InputLine = string(line[:len(line)-1])
			}
		case sys.InputLine == "" && (r == '+' || r == '-' || r == 'q' || r == 'Q'):
			sys.mu.Unlock()
			_ = sys.ExecuteDirective(string(r))
			continue
		case unicode.IsPrint(r) && len([]rune(sys.InputLine)) < 128:
			sys.InputLine += string(r)
		}
		sys.mu.Unlock()
	}
}

type ScheduledDirective struct {
	Tick uint64
	Text string
}

// Сценарий: такт директива. Команда применяется после указанного такта;
// такт 0 — после начальной загрузки, перед первым тактом модели.
func ReadDirectiveScript(path string) ([]ScheduledDirective, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []ScheduledDirective
	scan := bufio.NewScanner(file)
	line := 0
	for scan.Scan() {
		line++
		text := strings.TrimSpace(scan.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		parts := strings.Fields(text)
		if len(parts) < 2 {
			return nil, fmt.Errorf("сценарий, строка %d: ожидается такт и директива", line)
		}
		tick, e := strconv.ParseUint(parts[0], 10, 64)
		if e != nil || (len(events) > 0 && tick < events[len(events)-1].Tick) {
			return nil, fmt.Errorf("сценарий, строка %d: такты должны не убывать", line)
		}
		events = append(events, ScheduledDirective{tick, strings.Join(parts[1:], " ")})
	}
	return events, scan.Err()
}
func (sys *SystemModel) RunBatch(ticks int, events []ScheduledDirective) error {
	next := 0
	for i := 0; i <= ticks; i++ {
		for next < len(events) && events[next].Tick == sys.GlobalPC {
			event := events[next]
			if err := sys.ExecuteDirective(event.Text); err != nil {
				return fmt.Errorf("такт %d, %s: %w", event.Tick, event.Text, err)
			}
			next++
			if !sys.GetRunning() {
				return nil
			}
		}
		if i == ticks || !sys.GetRunning() {
			break
		}
		sys.StepSimulation()
	}
	return nil
}
