// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package vocabulary is the analyzer's fixture: one closed set and every shape
// a switch over it can take.
//
// Its import path is under this repository's own module prefix on purpose --
// that prefix is what the analyzer uses to tell a set it is responsible for
// from one the standard library grows on its own schedule, so a fixture
// anywhere else would be testing the skip rather than the check.
package vocabulary

// Colour is the closed set.
type Colour string

// The words of it. Three, so that a switch can be wrong about one and right
// about the others.
const (
	Red   Colour = "red"
	Green Colour = "green"
	Blue  Colour = "blue"
)

// Sole is a type with one constant, which is a sentinel and not a vocabulary.
type Sole int

// Only is that one constant.
const Only Sole = 1

func everyWord(c Colour) string {
	switch c {
	case Red:
		return "r"
	case Green:
		return "g"
	case Blue:
		return "b"
	}
	return ""
}

func oneMissing(c Colour) string {
	switch c { // want `switch on Colour does not name Blue`
	case Red:
		return "r"
	case Green:
		return "g"
	}
	return ""
}

func aDefaultIsNotAnAnswer(c Colour) string {
	switch c { // want `switch on Colour does not name Blue`
	case Red:
		return "r"
	case Green:
		return "g"
	default:
		return "?"
	}
}

func groupedCasesCount(c Colour) string {
	switch c {
	case Red, Green:
		return "warm"
	case Blue:
		return "cool"
	}
	return ""
}

// A directive on the function is not the switch's own comment, and the check
// says so by ignoring it; the one that counts sits immediately above.
func exemptedWithAReason(c Colour) string {
	//exhaustive:total blue is the fallback, and this sentence is the reason
	switch c {
	case Red:
		return "r"
	default:
		return "b"
	}
}

func exemptedWithoutOne(c Colour) string {
	//exhaustive:total
	switch c { // want `says nothing about why`
	case Red:
		return "r"
	default:
		return "b"
	}
}

func aSentinelIsNotAVocabulary(s Sole) string {
	switch s {
	case Only:
		return "only"
	}
	return ""
}

// A directive five statements away from the switch it was meant for is a
// sentence a reader would trust about the wrong code, so only the group
// immediately above one counts.
func aDirectiveOnTheFunctionDoesNotReachTheSwitch(c Colour) string {
	//exhaustive:total this marker is attached to the statement below it
	_ = c
	switch c { // want `switch on Colour does not name Blue`
	case Red:
		return "r"
	case Green:
		return "g"
	}
	return ""
}

func somebodyElsesSet(v any) string {
	switch v.(type) {
	case int:
		return "int"
	}
	return ""
}
