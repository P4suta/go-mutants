// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"fmt"
	"reflect"
)

// A Set is a value of type T together with the fact of whether anyone
// actually set it.
//
// Precedence needs that fact and cannot recover it from the value. `--jobs 0`,
// `strict = false`, and `formats = []` are all indistinguishable from their
// zero values, so a layer built out of plain fields would either overwrite the
// layer below it with values nobody asked for, or would quietly refuse to
// accept a deliberate zero. Recording presence separately is what lets a flag
// override the file exactly when the user typed the flag, which for the CLI
// means exactly when pflag reports the flag as Changed.
//
// The zero Set[T] is unset, so an [Overlay] needs no constructor. A Set[T] is
// a value type and is copied freely; when T is a slice, the usual aliasing
// caveats apply and [Merge] copies on the way out.
type Set[T any] struct {
	value   T
	present bool
}

// Explicit returns a Set holding v.
func Explicit[T any](v T) Set[T] { return Set[T]{value: v, present: true} }

// Unset returns the empty Set. It is the zero value, spelled out for the
// places where saying so is clearer than an elided struct field.
func Unset[T any]() Set[T] { return Set[T]{} }

// When returns a Set holding v when changed is true, and the empty Set
// otherwise. It is the shape a command-line flag arrives in:
//
//	overlay.Jobs = config.When(cmd.Flags().Changed("jobs"), jobs)
func When[T any](changed bool, v T) Set[T] {
	if !changed {
		return Set[T]{}
	}
	return Set[T]{value: v, present: true}
}

// IsSet reports whether the value was set.
func (s Set[T]) IsSet() bool { return s.present }

// Get returns the value and whether it was set. The value is T's zero value
// when it was not.
func (s Set[T]) Get() (T, bool) { return s.value, s.present }

// Or returns the value when it was set, and fallback otherwise.
func (s Set[T]) Or(fallback T) T {
	if !s.present {
		return fallback
	}
	return s.value
}

// Equal reports whether two Sets carry the same presence and, when present,
// values that reflect.DeepEqual accepts.
//
// It exists as much for go-cmp as for callers: the fields are unexported, so
// without an Equal method every precedence test would have to enumerate each
// instantiation of Set in cmp.AllowUnexported.
func (s Set[T]) Equal(other Set[T]) bool {
	if s.present != other.present {
		return false
	}
	if !s.present {
		return true
	}
	return reflect.DeepEqual(s.value, other.value)
}

// String renders the value, or "unset".
func (s Set[T]) String() string {
	if !s.present {
		return "unset"
	}
	return fmt.Sprintf("%v", s.value)
}
