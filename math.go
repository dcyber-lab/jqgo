package jqgo

import (
	"fmt"
	"math"
)

func registerMath() {
	unary := map[string]func(float64) float64{
		"sqrt": math.Sqrt, "cbrt": math.Cbrt, "exp": math.Exp, "exp2": math.Exp2,
		"exp10": func(x float64) float64 {
			if x == math.Trunc(x) && math.Abs(x) < 400 {
				return math.Pow10(int(x))
			}
			return math.Pow(10, x)
		},
		"pow10": func(x float64) float64 {
			if x == math.Trunc(x) && math.Abs(x) < 400 {
				return math.Pow10(int(x))
			}
			return math.Pow(10, x)
		},
		"expm1": math.Expm1, "log": math.Log, "log2": math.Log2, "log10": math.Log10,
		"log1p": math.Log1p, "logb": math.Logb,
		"sin": math.Sin, "cos": math.Cos, "tan": math.Tan,
		"asin": math.Asin, "acos": math.Acos, "atan": math.Atan,
		"sinh": math.Sinh, "cosh": math.Cosh, "tanh": math.Tanh,
		"asinh": math.Asinh, "acosh": math.Acosh, "atanh": math.Atanh,
		"fabs": math.Abs, "gamma": lgamma, "lgamma": lgamma, "tgamma": math.Gamma,
		"lgamma_r": lgamma, "j0": math.J0, "j1": math.J1, "y0": math.Y0, "y1": math.Y1,
		"significand": func(x float64) float64 {
			if x == 0 || math.IsInf(x, 0) || math.IsNaN(x) {
				return x
			}
			frac, _ := math.Frexp(x)
			return frac * 2
		},
	}
	for name, f := range unary {
		f := f
		defFn(name, 0, func(e *evaluator, v any, _ []any) (any, error) {
			x, ok := toFloat(v)
			if !ok {
				return nil, errNumberRequired(v)
			}
			return f(x), nil
		})
	}
	// Rounding functions keep integers as ints.
	rounding := map[string]func(float64) float64{
		"floor": math.Floor, "ceil": math.Ceil, "round": math.Round, "trunc": math.Trunc,
		"rint": math.RoundToEven, "nearbyint": math.RoundToEven,
	}
	for name, f := range rounding {
		f := f
		defFn(name, 0, func(e *evaluator, v any, _ []any) (any, error) {
			switch x := v.(type) {
			case int:
				return x, nil
			case float64:
				r := f(x)
				if math.IsInf(r, 0) || math.IsNaN(r) {
					return r, nil
				}
				return intIfExact(r), nil
			}
			return nil, errNumberRequired(v)
		})
	}
	binary := map[string]func(float64, float64) float64{
		"pow": math.Pow, "atan2": math.Atan2, "fmin": math.Min, "fmax": math.Max,
		"fmod": math.Mod, "copysign": math.Copysign, "drem": math.Remainder,
		"nextafter": math.Nextafter, "fdim": math.Dim, "hypot": math.Hypot,
		"ldexp":   func(a, b float64) float64 { return math.Ldexp(a, int(b)) },
		"scalb":   func(a, b float64) float64 { return a * math.Pow(2, b) },
		"scalbln": func(a, b float64) float64 { return math.Ldexp(a, int(b)) },
	}
	for name, f := range binary {
		f := f
		defFn(name, 2, func(e *evaluator, v any, args []any) (any, error) {
			a, ok1 := toFloat(args[0])
			b, ok2 := toFloat(args[1])
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("%s: number required", name)
			}
			r := f(a, b)
			if name == "pow" {
				if _, ok := args[0].(int); ok {
					if _, ok := args[1].(int); ok {
						return intIfExact(r), nil
					}
				}
			}
			return r, nil
		})
	}
	defFn("fma", 3, func(e *evaluator, v any, args []any) (any, error) {
		a, ok1 := toFloat(args[0])
		b, ok2 := toFloat(args[1])
		c, ok3 := toFloat(args[2])
		if !ok1 || !ok2 || !ok3 {
			return nil, fmt.Errorf("fma: number required")
		}
		return math.FMA(a, b, c), nil
	})
	defFn("frexp", 0, func(e *evaluator, v any, _ []any) (any, error) {
		x, ok := toFloat(v)
		if !ok {
			return nil, errNumberRequired(v)
		}
		f, exp := math.Frexp(x)
		return []any{f, exp}, nil
	})
	defFn("modf", 0, func(e *evaluator, v any, _ []any) (any, error) {
		x, ok := toFloat(v)
		if !ok {
			return nil, errNumberRequired(v)
		}
		i, f := math.Modf(x)
		return []any{f, i}, nil
	})
}

func lgamma(x float64) float64 {
	r, _ := math.Lgamma(x)
	return r
}
