// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"fmt"
	"reflect"
)

type Set[T any] struct {
	value   T
	present bool
}

func Explicit[T any](v T) Set[T] { return Set[T]{value: v, present: true} }

func Unset[T any]() Set[T] { return Set[T]{} }

func When[T any](changed bool, v T) Set[T] {
	if !changed {
		return Set[T]{}
	}
	return Set[T]{value: v, present: true}
}

func (s Set[T]) IsSet() bool { return s.present }

func (s Set[T]) Get() (T, bool) { return s.value, s.present }

func (s Set[T]) Or(fallback T) T {
	if !s.present {
		return fallback
	}
	return s.value
}

func (s Set[T]) Equal(other Set[T]) bool {
	if s.present != other.present {
		return false
	}
	if !s.present {
		return true
	}
	return reflect.DeepEqual(s.value, other.value)
}

func (s Set[T]) String() string {
	if !s.present {
		return "unset"
	}
	return fmt.Sprintf("%v", s.value)
}
