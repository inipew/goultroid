package shell

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/inipew/goultroid/internal/settings"
)

var ErrMutationUnsupported = errors.New("assistant/shell: setting mutation unsupported")

type MutationOperation string

const (
	MutationChange   MutationOperation = "change"
	MutationDecrease MutationOperation = "decrease"
	MutationIncrease MutationOperation = "increase"
	MutationReset    MutationOperation = "reset"
	MutationInput    MutationOperation = "input"
)

const (
	SettingsInputTTL      = 2 * time.Minute
	MaxSettingsInputBytes = 4096
)

type MutationOutcome string

const (
	MutationChanged MutationOutcome = "changed"
	MutationNoop    MutationOutcome = "noop"
)

type MutationResult struct {
	Namespace string
	Key       string
	Operation MutationOperation
	Outcome   MutationOutcome
	Previous  string
	Persisted string
	Effective string
	Source    string
}

type MutationStage string

const (
	MutationStageBinding MutationStage = "binding"
	MutationStageReserve MutationStage = "reserve_revision"
	MutationStagePersist MutationStage = "persist"
	MutationStageRender  MutationStage = "render"
)

type MutationError struct {
	Stage     MutationStage
	Committed bool
	Result    MutationResult
	Err       error
}

func (e *MutationError) Error() string {
	if e == nil {
		return "assistant/shell: setting mutation failed"
	}
	return fmt.Sprintf("assistant/shell: setting mutation failed at %s: %v", e.Stage, e.Err)
}

func (e *MutationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type MutationPlan struct {
	Operation MutationOperation
	Value     string
	Outcome   MutationOutcome
}

func SafeMutationValue(def settings.SettingDefinition, value string) string {
	if def.Sensitive && value != "" {
		return "••••"
	}
	return value
}

func PlanMutation(def settings.SettingDefinition, current string, explicitUser bool, operation MutationOperation) (MutationPlan, error) {
	if operation == MutationReset {
		if !explicitUser {
			return MutationPlan{Operation: operation, Outcome: MutationNoop}, nil
		}
		return MutationPlan{Operation: operation, Outcome: MutationChanged}, nil
	}

	direction := int64(1)
	switch operation {
	case MutationChange, MutationIncrease:
	case MutationDecrease:
		direction = -1
	default:
		return MutationPlan{}, fmt.Errorf("%w: operation %q", ErrMutationUnsupported, operation)
	}

	var next string
	switch def.Type {
	case settings.TypeBool:
		if operation != MutationChange {
			return MutationPlan{}, fmt.Errorf("%w: bool requires change", ErrMutationUnsupported)
		}
		value, err := strconv.ParseBool(current)
		if err != nil {
			return MutationPlan{}, err
		}
		next = strconv.FormatBool(!value)
	case settings.TypeEnum:
		if operation != MutationChange {
			return MutationPlan{}, fmt.Errorf("%w: enum requires change", ErrMutationUnsupported)
		}
		if len(def.AllowedValues) == 0 {
			return MutationPlan{}, fmt.Errorf("enum %s:%s has no values", def.Namespace, def.Key)
		}
		next = def.AllowedValues[0]
		for index, value := range def.AllowedValues {
			if value == current {
				next = def.AllowedValues[(index+1)%len(def.AllowedValues)]
				break
			}
		}
	case settings.TypeInt:
		if operation == MutationChange {
			return MutationPlan{}, fmt.Errorf("%w: int requires increase/decrease", ErrMutationUnsupported)
		}
		value, err := strconv.ParseInt(current, 10, 64)
		if err != nil {
			return MutationPlan{}, err
		}
		step := def.UI.Step
		if step <= 0 {
			step = 1
		}
		value += direction * step
		if def.MaxVal != nil && value > *def.MaxVal {
			if def.MinVal != nil {
				value = *def.MinVal
			} else {
				value = *def.MaxVal
			}
		}
		if def.MinVal != nil && value < *def.MinVal {
			if def.MaxVal != nil {
				value = *def.MaxVal
			} else {
				value = *def.MinVal
			}
		}
		next = strconv.FormatInt(value, 10)
	case settings.TypeDuration:
		if operation == MutationChange {
			return MutationPlan{}, fmt.Errorf("%w: duration requires increase/decrease", ErrMutationUnsupported)
		}
		value, err := time.ParseDuration(current)
		if err != nil {
			return MutationPlan{}, err
		}
		step := time.Duration(def.UI.Step) * time.Second
		if step <= 0 {
			step = 5 * time.Second
		}
		value += time.Duration(direction) * step
		if def.MaxVal != nil && int64(value/time.Second) > *def.MaxVal {
			value = time.Duration(*def.MaxVal) * time.Second
		}
		if def.MinVal != nil && int64(value/time.Second) < *def.MinVal {
			value = time.Duration(*def.MinVal) * time.Second
		}
		next = value.String()
	case settings.TypeString:
		return MutationPlan{}, fmt.Errorf("%w: string requires text input", ErrMutationUnsupported)
	default:
		return MutationPlan{}, fmt.Errorf("%w: type %q", ErrMutationUnsupported, def.Type)
	}

	canonical, err := def.Canonicalize(next)
	if err != nil {
		return MutationPlan{}, err
	}
	outcome := MutationChanged
	if canonical == current {
		outcome = MutationNoop
	}
	return MutationPlan{Operation: operation, Value: canonical, Outcome: outcome}, nil
}
