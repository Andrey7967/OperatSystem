package main

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strconv"
	"strings"
)

type ParameterID int

const (
	ParamMemory ParameterID = iota
	ParamSpeed
	ParamQuantum
	ParamGlobal
	ParamTaskSize
	ParamTaskCommands
	ParamTaskIO
	ParamTaskIODuration
	ParamTaskPriority
	ParameterCount
)

var parameterNames = [ParameterCount]string{
	"memory", "speed", "quantum", "global",
	"task_size", "task_commands", "task_io", "task_io_duration", "task_priority",
}

type ParameterRange struct{ Min, Max, Step int }

// Границы включены. При невыровненном Max последняя точка сетки меньше Max.
// Нулевой шаг допустим только для константы; обратные границы нормализуются.
func (r ParameterRange) Normalize() (ParameterRange, error) {
	if r.Min < 0 || r.Min > 65535 || r.Max < 0 || r.Max > 65535 || r.Step < 0 || r.Step > 65535 {
		return r, fmt.Errorf("числа должны лежать в диапазоне word 0..65535")
	}
	if r.Min > r.Max {
		r.Min, r.Max = r.Max, r.Min
	}
	if r.Step == 0 && r.Min != r.Max {
		return r, fmt.Errorf("нулевой шаг допустим только при равных границах")
	}
	return r, nil
}

// Вызывается для нормализованного диапазона из проверенной конфигурации.
func GenerateParameterValue(r ParameterRange, rng *rand.Rand) int {
	if r.Min == r.Max {
		return r.Min
	}
	count := (r.Max-r.Min)/r.Step + 1
	return r.Min + rng.Intn(count)*r.Step
}

type ParameterConfig struct {
	Ranges   [ParameterCount]ParameterRange
	Comments [ParameterCount]string
}

func (c ParameterConfig) Validate() error {
	for id, r := range c.Ranges {
		normalized, err := r.Normalize()
		if err != nil {
			return fmt.Errorf("%s: %w", parameterNames[id], err)
		}
		if normalized != r {
			return fmt.Errorf("%s: диапазон не нормализован", parameterNames[id])
		}
		switch ParameterID(id) {
		case ParamMemory, ParamSpeed, ParamQuantum, ParamTaskSize, ParamTaskCommands, ParamTaskIODuration:
			if r.Min < 1 {
				return fmt.Errorf("%s: минимальное значение должно быть не меньше 1", parameterNames[id])
			}
		case ParamTaskIO:
			if r.Max > 100 {
				return fmt.Errorf("task_io: вероятность должна лежать в 0..100")
			}
		}
	}
	return nil
}

// Формат: min max step key комментарий. Порядок строк произвольный.
func ParseParameterFile(reader io.Reader) (ParameterConfig, error) {
	var c ParameterConfig
	var seen [ParameterCount]bool
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if line == 1 {
			text = strings.TrimPrefix(text, "\uFEFF")
		}
		fields := strings.Fields(text)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if len(fields) < 5 {
			return c, fmt.Errorf("строка %d: нужны min max step key и комментарий", line)
		}
		id := -1
		for i, name := range parameterNames {
			if fields[3] == name {
				id = i
				break
			}
		}
		if id < 0 {
			return c, fmt.Errorf("строка %d: неизвестный параметр %q", line, fields[3])
		}
		if seen[id] {
			return c, fmt.Errorf("строка %d: повтор параметра %s", line, fields[3])
		}
		var values [3]int
		for i := range values {
			n, err := strconv.ParseUint(fields[i], 10, 16)
			if err != nil {
				return c, fmt.Errorf("строка %d: %q не является числом word 0..65535", line, fields[i])
			}
			values[i] = int(n)
		}
		r, err := (ParameterRange{values[0], values[1], values[2]}).Normalize()
		if err != nil {
			return c, fmt.Errorf("строка %d (%s): %w", line, fields[3], err)
		}
		c.Ranges[id] = r
		c.Comments[id] = strings.Join(fields[4:], " ")
		seen[id] = true
	}
	if err := scanner.Err(); err != nil {
		return c, fmt.Errorf("чтение файла параметров: %w", err)
	}
	for i, ok := range seen {
		if !ok {
			return c, fmt.Errorf("отсутствует параметр %s", parameterNames[i])
		}
	}
	return c, c.Validate()
}

// Единственный доступ к файлу: main вызывает эту функцию один раз при старте.
func ReadParameterFile(path string) (ParameterConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return ParameterConfig{}, fmt.Errorf("файл параметров %q: %w", path, err)
	}
	defer f.Close()
	return ParseParameterFile(f)
}

type GeneratedSystemParameters struct{ Memory, Speed, Quantum, Global int }

func GenerateSystemParameters(c ParameterConfig, rng *rand.Rand) GeneratedSystemParameters {
	return GeneratedSystemParameters{
		Memory:  GenerateParameterValue(c.Ranges[ParamMemory], rng),
		Speed:   GenerateParameterValue(c.Ranges[ParamSpeed], rng),
		Quantum: GenerateParameterValue(c.Ranges[ParamQuantum], rng),
		Global:  GenerateParameterValue(c.Ranges[ParamGlobal], rng),
	}
}

func NewConfiguredSystemModel(seed int64, config ParameterConfig) (*SystemModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return newSystemModel(seed, &config), nil
}

func (sys *SystemModel) TaskIOParameters(p *PSW) (percent, duration int) {
	if p.HasTaskParameters {
		return p.IOPercent, p.IODuration
	}
	return sys.IOProbability, sys.NInOut // Совместимость контрольных задач ЛР5–7.
}
