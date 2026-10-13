package main

import (
	"encoding/csv"
	"fmt"
	"os"
)

// Снимок до освобождения PSW: повторное использование слота его не изменяет.
type TaskMeasurement struct {
	TaskID                                            int
	Success                                           bool
	LoadedTick, FinishedTick                          uint64
	Mono, Multi, Commands, IO, Ready, Blocked, Global uint64
}

type PerformanceParameters struct {
	Completed                                uint64
	MeanMono, MeanMulti, MonoTasks, Relative float64
}

// Вызывать под mutex модели. Такты службы, уже включённые в очередной
// StepSimulation, повторно сюда не передаются: иначе ожидание удвоится.
func (sys *SystemModel) IncreaseReadyProcessTime(ticks uint64) {
	for slot, p := range sys.ProcessTable {
		m := &sys.Metrics[slot]
		if p.State == StateReady && !p.Suspended && p.TaskID == m.TaskID && m.TaskID != 0 {
			m.ReadyTicks += ticks
		}
	}
}

func executionIncrease(mono, multi uint64) float64 {
	if mono == 0 {
		return 0
	}
	// Вычитание после преобразования: uint64 не допускает отрицательную разность.
	return 100 * (float64(multi)/float64(mono) - 1)
}

func (sys *SystemModel) Performance() PerformanceParameters {
	n := sys.Stats.Completed - sys.Stats.Failed
	p := PerformanceParameters{Completed: n,
		MeanMono: average(sys.Stats.MonoSum, n), MeanMulti: average(sys.Stats.MultiSum, n)}
	if p.MeanMono > 0 && sys.GlobalPC > 0 {
		p.MonoTasks = float64(sys.GlobalPC) / p.MeanMono
		p.Relative = 100 * float64(n) / p.MonoTasks
	}
	return p
}

func (sys *SystemModel) ExportMeasurements(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	err = w.Write([]string{"task", "success", "loaded", "finished", "T_mono", "T_multi", "commands", "io_ticks", "ready_ticks", "blocked_ticks", "global_ticks", "D_exe_percent", "D_ready_percent"})
	for _, m := range sys.Measurements {
		if err != nil {
			break
		}
		err = w.Write([]string{fmt.Sprint(m.TaskID), fmt.Sprint(m.Success),
			fmt.Sprint(m.LoadedTick), fmt.Sprint(m.FinishedTick), fmt.Sprint(m.Mono), fmt.Sprint(m.Multi),
			fmt.Sprint(m.Commands), fmt.Sprint(m.IO), fmt.Sprint(m.Ready), fmt.Sprint(m.Blocked), fmt.Sprint(m.Global),
			fmt.Sprintf("%.6f", executionIncrease(m.Mono, m.Multi)), fmt.Sprintf("%.6f", 100*average(m.Ready, m.Multi))})
	}
	w.Flush()
	if err == nil {
		err = w.Error()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
