package browser

import (
	"context"

	"github.com/chromedp/chromedp"
)

// storeAction lets a sequence retain a typed observation without opening a
// second browser context or changing the order of its actions.
func storeAction[T any](action chromedp.Action[T], destination *T) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		value, err := action(ctx, target)
		if err == nil {
			*destination = value
		}
		return err
	})
}

func evaluateInto[T any](expression string, destination *T, options ...chromedp.EvaluateOption) chromedp.Action[chromedp.Void] {
	return storeAction(chromedp.Evaluate[T](expression, options...), destination)
}
