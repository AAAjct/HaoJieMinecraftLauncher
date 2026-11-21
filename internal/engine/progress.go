package engine

import (
	"context"
	"sync"
)

type StageProgress struct {
	Label          string
	Fraction       float64
	Remaining      int
	BytesPerSecond float64
	Planning       bool
}

type progressSinkKey struct{}
type progressLabelKey struct{}

func WithStageProgress(ctx context.Context, report func([]StageProgress)) context.Context {
	return context.WithValue(ctx, progressSinkKey{}, report)
}

func reportStage(ctx context.Context, progress StageProgress) {
	if report, ok := ctx.Value(progressSinkKey{}).(func([]StageProgress)); ok {
		if label, ok := ctx.Value(progressLabelKey{}).(string); ok {
			progress.Label = label
		}
		report([]StageProgress{progress})
	}
}

func planStages(ctx context.Context, labels ...string) {
	if report, ok := ctx.Value(progressSinkKey{}).(func([]StageProgress)); ok {
		stages := make([]StageProgress, 0, len(labels))
		for _, label := range labels {
			stages = append(stages, StageProgress{Label: label, Fraction: -1, Remaining: -1})
		}
		report(stages)
	}
}

func pendingStage(ctx context.Context, label string) {
	reportStage(ctx, StageProgress{Label: label, Fraction: -1, Remaining: -1})
}

func trackStage(ctx context.Context, label string, work func(context.Context) error) error {
	ctx = context.WithValue(ctx, progressLabelKey{}, label)
	pendingStage(ctx, label)
	if err := work(ctx); err != nil {
		return err
	}
	reportStage(ctx, StageProgress{Label: label, Fraction: 1})
	return nil
}

func runParallel(ctx context.Context, status func(string), jobs ...func(context.Context, func(string)) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	report := func(message string) {
		mu.Lock()
		defer mu.Unlock()
		status(message)
	}
	for _, job := range jobs {
		wg.Go(func() {
			if err := job(ctx, report); err != nil {
				cancel(err)
			}
		})
	}
	wg.Wait()
	return context.Cause(ctx)
}
