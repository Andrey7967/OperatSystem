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
    Code    OperationCode
    Address1 int
    Address2 int
    ResultAddress int
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

    Memory      []int // Память процесса, Size ячеек
    LastCommand Command // Последняя выполненная команда
    IORemaining int // Оставшееся время операции ввода/вывода

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
    LastCommand  OperationCode // Тип последней команды на этом ЦП
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
    IOProbability int // Процент IO среди команд до EXIT
    NInOut        int // Длительность IO в тактах после инициализации

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
        IOProbability: 20,
        NInOut: 3,

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
    fmt.Println("  Вариант 5: 3 ЦП, относительные и динамические приоритеты")
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
    newTask.Memory = make([]int, newTask.Size)
    for i := range newTask.Memory {
        newTask.Memory[i] = newTask.TaskID + i
    }
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
        return false
    }

    proc := &sys.ProcessTable[bestIdx]

    // Переводим процесс в Active
    proc.State = StateActive

    // При повторном назначении сохраняем накопленное снижение приоритета.
    proc.QuantumUsed = 0

    sys.RestoreProcessState(cpu, bestIdx)

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

    sys.SaveProcessState(cpu)
    sys.UpdateProcessPriority(proc)

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
    if sys.RNG.Intn(100) < sys.IOProbability {
        return Command{Code: OpIO}
    }
    size := len(proc.Memory)
    if size == 0 {
        return Command{Code: OpCalculate, Address1: -1}
    }
    return Command{
        Code: OpCalculate,
        Address1: (cpu.PC + 1) % size,
        Address2: (cpu.PC + 2) % size,
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

// Контекст сохраняется до освобождения ЦП. IO выполняется без участия ЦП.
func (sys *SystemModel) InitializeIO(cpu *CPU) {
    sys.SaveProcessState(cpu)
    proc := &sys.ProcessTable[cpu.ActiveProcID]
    proc.State = StateInitIO
    proc.IORemaining = sys.NInOut
    proc.QuantumUsed = 0
    sys.LastAction = fmt.Sprintf("Процесс #%d: инициализация IO, %d такта", proc.TaskID, sys.NInOut)
    cpu.ActiveProcID = -1
    cpu.PC = 0
    cpu.IsBusy = false
}

// Минимальная модель независимой IO-операции каждого процесса.
// Первый такт после обращения отведён инициализации; затем NInOut тактов IO.
func (sys *SystemModel) StepIO() {
    for i := range sys.ProcessTable {
        proc := &sys.ProcessTable[i]
        if proc.State == StateInitIO {
            proc.State = StateBlocked
        } else if proc.State == StateBlocked {
            proc.IORemaining--
            if proc.IORemaining <= 0 {
                proc.IORemaining = 0
                proc.State = StateReady
                sys.LastAction = fmt.Sprintf("Процесс #%d: IO завершён, состояние Готов", proc.TaskID)
            }
        }
    }
}

func (sys *SystemModel) DoOperation(cpu *CPU, code OperationCode, operand1, operand2 int) (int, error) {
    switch code {
    case OpCalculate:
        return operand1 + operand2, nil
    case OpIO:
        sys.InitializeIO(cpu)
    case OpExit:
        sys.FinishProcess(cpu)
        // После освобождения памяти загрузить столько заданий, сколько помещается.
        for sys.LoadTask() {
        }
    default:
        return 0, fmt.Errorf("неизвестная команда %q", code)
    }
    return 0, nil
}

// Ошибочная команда или адрес приводят к выгрузке процесса, а не к panic.
func (sys *SystemModel) AbortProcess(cpu *CPU, err error) {
    taskID := sys.ProcessTable[cpu.ActiveProcID].TaskID
    sys.FinishProcess(cpu)
    for sys.LoadTask() {
    }
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

    // Увеличиваем глобальный счетчик
    sys.GlobalPC++

    sys.StepIO()

    // --------------------------------------------------------
    // Периодически создаём новые процессы
    // --------------------------------------------------------

    if sys.GlobalPC == 1 || sys.GlobalPC%20 == 0 {
        sys.LoadTask()
    }

    // --------------------------------------------------------
    // Назначаем процессы на все свободные ЦП до выполнения команд
    // --------------------------------------------------------

    sys.DispatchReadyProcesses()

    // --------------------------------------------------------
    // Работа трех CPU
    // --------------------------------------------------------

    for i := 0; i < len(sys.CPUs); i++ {
        cpu := &sys.CPUs[i]

        // ----------------------------------------------------
        // Если CPU занят — выполняем одну команду
        // ----------------------------------------------------

        if cpu.IsBusy && cpu.ActiveProcID != -1 {
            proc := &sys.ProcessTable[cpu.ActiveProcID]

            // Полный цикл генерации, разбора и исполнения одной команды.
            sys.ExecuteCommand(cpu)
            if !cpu.IsBusy {
                continue
            }

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
    // Новые назначения исполнят первую команду только в следующем такте.
    sys.DispatchReadyProcesses()
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
                "Работа (Слот=%02d, TaskID=%d, PC=%d, Effective=%d)",
                cpu.ActiveProcID,
                proc.TaskID,
                cpu.PC,
                sys.EffectivePriority(proc),
            )
        }

        printfLine(
            "   ЦП #%d: %s | Последняя команда: %s",
            cpu.ID,
            status,
            cpu.LastCommand,
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
            "       Effective=%d | Команда=%s | IO осталось=%d",
            sys.EffectivePriority(&proc),
            proc.LastCommand.Code,
            proc.IORemaining,
        )
    }

    // Очищаем оставшиеся строки
    for i := renderedRows; i < 6; i++ {
        b.WriteString("\033[K\n")
        b.WriteString("\033[K\n")
    }

    printlnLine("=========================================================================")

    printfLine("Effective = Base + Dyn | Квант = %d | IO = %d%%, %d такта",
        sys.Kvant, sys.IOProbability, sys.NInOut)

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


