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
// Состояния процесса
// ============================================================

type ProcessState string

const (
    StateAbsent  ProcessState = "Отсутствует"
    StateReady   ProcessState = "Готов"
    StateActive  ProcessState = "Активен"
    StateBlocked ProcessState = "Блокирован"
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
    default:
        return -1
    }
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
    DynPriority  int // Динамическая надбавка к базовому приоритету

    QuantumUsed int // Сколько команд выполнено в текущем кванте

    State ProcessState // Состояние процесса
}

// ============================================================
// CPU — Центральный процессор
// ============================================================

type CPU struct {
    ID           int
    ActiveProcID int
    PC           int
    IsBusy       bool
}

// ============================================================
// SystemModel — модель операционной системы
// ============================================================

type SystemModel struct {
    mu sync.Mutex

    TotalMemory int
    FreeMemory  int

    Speed float64
    Kvant int

    GlobalPC uint64

    IsRunning bool
    PauseLoad bool

    NextTaskID int

    CPUs []CPU

    ProcessTable [16]PSW
    PSW_Task     PSW // Подготовленное задание, ожидающее загрузки

    LastAction string

    RNG *rand.Rand
}

// ============================================================
// Создание системы
// ============================================================

func NewSystemModel() *SystemModel {
    rng := rand.New(rand.NewSource(time.Now().UnixNano()))

    sys := &SystemModel{
        TotalMemory: 1024*2,
        FreeMemory:  1024*2,

        Speed: 10.0,
        Kvant: 10,

        GlobalPC: 0,

        IsRunning: true,
        PauseLoad: false,

        NextTaskID: 1,

        CPUs: make([]CPU, 3),

        LastAction: "Ожидание ввода...",

        RNG: rng,
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
        sys.ProcessTable[i] = PSW{
            State: StateAbsent,
        }
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
    fmt.Println("  Вариант 5: 3 ЦП, базовые и динамические приоритеты")
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

        State: StateReady,
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
    sys.ProcessTable[freeSlot] = newTask

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

func (sys *SystemModel) FindHighestPriorityReadyProcess() int {
    bestIdx := -1
    bestPriority := -1

    for i := 0; i < len(sys.ProcessTable); i++ {
        proc := &sys.ProcessTable[i]

        if proc.State != StateReady {
            continue
        }

        effectivePriority := sys.EffectivePriority(proc)

        // Выбираем максимальный итоговый приоритет
        if effectivePriority > bestPriority {
            bestPriority = effectivePriority
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

    // Ищем процесс с максимальным:
    // BasePriority + DynPriority
    bestIdx := sys.FindHighestPriorityReadyProcess()

    if bestIdx == -1 {
        return false
    }

    proc := &sys.ProcessTable[bestIdx]

    // Переводим процесс в Active
    proc.State = StateActive

    // Когда процесс получил CPU,
    // динамическую надбавку за ожидание сбрасываем
    proc.DynPriority = 0

    proc.QuantumUsed = 0

    cpu.ActiveProcID = bestIdx
    cpu.PC = proc.PC
    cpu.IsBusy = true

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

    proc := &sys.ProcessTable[cpu.ActiveProcID]

    taskID := proc.TaskID

    sys.LastAction = fmt.Sprintf(
        "Процесс #%d завершён на ЦП #%d",
        taskID,
        cpu.ID,
    )

    // Освобождаем память
    sys.FreeMemory += proc.Size

    // Освобождаем слот
    sys.ProcessTable[cpu.ActiveProcID] = PSW{
        State: StateAbsent,
    }

    // Освобождаем CPU
    cpu.ActiveProcID = -1
    cpu.PC = 0
    cpu.IsBusy = false
}

// ============================================================
// Окончание кванта
// ============================================================

func (sys *SystemModel) EndQuantum(cpu *CPU) {
    if cpu.ActiveProcID == -1 {
        return
    }

    proc := &sys.ProcessTable[cpu.ActiveProcID]

    // Если процесс уже завершился
    if proc.PC >= proc.TotalCommands {
        sys.FinishProcess(cpu)
        return
    }

    taskID := proc.TaskID
    oldEffective := sys.EffectivePriority(proc)

    // Возвращаем процесс в READY
    proc.State = StateReady
    proc.QuantumUsed = 0

    // CPU освобождается
    cpu.ActiveProcID = -1
    cpu.PC = 0
    cpu.IsBusy = false

    sys.LastAction = fmt.Sprintf(
        "Процесс #%d: квант завершён (Effective=%d), возвращён в Ready",
        taskID,
        oldEffective,
    )
}

// ============================================================
// Динамический приоритет
//
// Каждый процесс, находящийся в READY,
// получает +1 к DynPriority за такт ожидания.
//
// Ограничиваем динамическую часть сверху 10.
//
// Таким образом:
//
//     Effective = Base + Dyn
//
// Базовый приоритет остаётся постоянным,
// динамический меняется в зависимости от ожидания.
// ============================================================

func (sys *SystemModel) UpdateWaitingPriorities() {
    for i := 0; i < len(sys.ProcessTable); i++ {
        proc := &sys.ProcessTable[i]

        if proc.State != StateReady {
            continue
        }

        // Максимальная динамическая надбавка = 10
        if proc.DynPriority < 10 {
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

    // Увеличиваем глобальный счетчик
    sys.GlobalPC++

    // --------------------------------------------------------
    // Периодически создаём новые процессы
    // --------------------------------------------------------

    if sys.GlobalPC == 1 || sys.GlobalPC%20 == 0 {
        sys.LoadTask()
    }

    // --------------------------------------------------------
    // Увеличиваем динамический приоритет ожидающих процессов
    // --------------------------------------------------------

    sys.UpdateWaitingPriorities()

    // --------------------------------------------------------
    // Работа трех CPU
    // --------------------------------------------------------

    for i := 0; i < len(sys.CPUs); i++ {
        cpu := &sys.CPUs[i]

        // ----------------------------------------------------
        // Если CPU свободен — выбираем процесс
        // с максимальным Base + Dyn
        // ----------------------------------------------------

        if !cpu.IsBusy {
            sys.DispatchProcess(cpu)
        }

        // ----------------------------------------------------
        // Если CPU занят — выполняем одну команду
        // ----------------------------------------------------

        if cpu.IsBusy && cpu.ActiveProcID != -1 {
            proc := &sys.ProcessTable[cpu.ActiveProcID]

            // Выполняем одну команду
            cpu.PC++
            proc.PC++

            // Увеличиваем использование кванта
            proc.QuantumUsed++

            // ------------------------------------------------
            // Проверяем завершение процесса
            // ------------------------------------------------

            if proc.PC >= proc.TotalCommands {
                sys.FinishProcess(cpu)
                continue
            }

            // ------------------------------------------------
            // Проверяем завершение кванта
            // ------------------------------------------------

            if proc.QuantumUsed >= sys.Kvant {
                sys.EndQuantum(cpu)
            }
        }
    }
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

func (sys *SystemModel) RenderUI() {
    sys.mu.Lock()
    defer sys.mu.Unlock()

    var b strings.Builder

    // Переход в левый верхний угол
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

    printfLine(
        "МОДЕЛЬ ОПЕРАЦИОННОЙ СИСТЕМЫ | Вариант 5 (3 ЦП)",
    )

    printlnLine("=========================================================================")

    printfLine(
        "[Системный PC]: %-10d | [Скорость]: %-6.1f тактов/сек",
        sys.GlobalPC,
        sys.Speed,
    )

    printfLine(
        "[Память ОЗУ]  : %d / %d (Свободно: %d)",
        sys.TotalMemory-sys.FreeMemory,
        sys.TotalMemory,
        sys.FreeMemory,
    )

    printlnLine("-------------------------------------------------------------------------")

    // =========================================================
    // CPU
    // =========================================================

    printlnLine("СОСТОЯНИЕ ЦЕНТРАЛЬНЫХ ПРОЦЕССОРОВ (ЦП):")

    for _, cpu := range sys.CPUs {
        status := "Ожидание"

        if cpu.IsBusy && cpu.ActiveProcID != -1 {
            proc := &sys.ProcessTable[cpu.ActiveProcID]

            status = fmt.Sprintf(
                "Работа (Процесс #%d, PC=%d, Effective=%d)",
                proc.TaskID,
                cpu.PC,
                sys.EffectivePriority(proc),
            )
        }

        printfLine(
            "   ЦП #%d: %s",
            cpu.ID,
            status,
        )
    }

    printlnLine("-------------------------------------------------------------------------")

    // =========================================================
    // Таблица процессов
    // =========================================================

    printlnLine("ТАБЛИЦА ПРОЦЕССОВ (PSW):")

    printfLine(
        " %-4s %-8s %-12s %-4s %-12s %-8s %-8s %-8s",
        "Слот",
        "TaskID",
        "Состояние",
        "Код",
        "PC/Total",
        "Размер",
        "Base",
        "Dyn",
    )

    // printfLine(
    //  " %-4s %-8s %-12s %-12s %-8s %-8s %-8s",
    //  "",
    //  "",
    //  "",
    //  "",
    //  "",
    //  "",
    //  "Effective",
    // )

    renderedRows := 0

    for i, proc := range sys.ProcessTable {
        if proc.State == StateAbsent {
            continue
        }

        renderedRows++

        printfLine(
            " [%02d] %-8d %-12s %-4d %d/%-7d %-8d %-8d %-8d",
            i,
            proc.TaskID,
            proc.State,
            proc.State.Code(),
            proc.PC,
            proc.TotalCommands,
            proc.Size,
            proc.BasePriority,
            proc.DynPriority,
        )

        // Отдельной строкой итоговый приоритет
        printfLine(
            "       Effective Priority = %d",
            sys.EffectivePriority(&proc),
        )
    }

    // Очищаем оставшиеся строки
    for i := renderedRows; i < 6; i++ {
        b.WriteString("\033[K\n")
        b.WriteString("\033[K\n")
    }

    printlnLine("=========================================================================")

    printlnLine(
        "Приоритет: Effective = Base + Dyn | Квант = 10 команд",
    )

    printlnLine(
        "[+] Скорость +10% | [-] Скорость -10% | [q] Выход",
    )

    printlnLine("=========================================================================")

    printfLine(
        "[Статус команды]: %s",
        sys.LastAction,
    )

    fmt.Print(b.String())
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

    model := NewSystemModel()

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

    // --------------------------------------------------------
    // Завершение
    // --------------------------------------------------------

    CleanupTerminal()

    fmt.Println(
        "\nМоделирование завершено по команде оператора ('q').",
    )
}


