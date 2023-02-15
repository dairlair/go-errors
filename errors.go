package errors

import (
	"fmt"
	"reflect"
	"runtime"
	"unsafe"

	"github.com/pkg/errors"
)

// We need to export certain functions or variables from the pkg/errors package
// so that they can be used by others.
// However, we want to ensure that the entrypoints through which these functions
// or variables are accessed have been thoroughly vetted.
// Therefore, only these specific entrypoints will be made available for use.
var (
	As     = errors.As
	Is     = errors.Is
	Cause  = errors.Cause
	Unwrap = errors.Unwrap
)

// To ensure compatibility with all of the functions that we export
// from the pkg/errors package, we need to make StackTrace an alias
// rather than creating it as a new type.
// By making it an alias, it will be easier to work with and can
// be used seamlessly with any of our exported functions.
type StackTrace = errors.StackTrace

type StackTracer interface {
	StackTrace() errors.StackTrace
}

// The purpose of the Sentinel function is to create compile-time errors
// that are intended to be treated as values without any
// associated stack trace.
// This function takes in a message and optional arguments, formats them
// using fmt.Errorf, and returns the resulting error.
func Sentinel(msg string, args ...interface{}) error {
	return fmt.Errorf(msg, args...)
}

// New acts creates a stack traced error from a message with interpolated parameters.
// It can be used when you want the stack trace to begin at the point where the error was created.
func New(msg string, args ...interface{}) error {
	return PopStack(errors.New(fmt.Sprintf(msg, args...)))
}

// Wrap creates a new error by decorating the original error message with a prefix.
// It differs from the Wrap and Wrapf functions in the pkg/errors package in that
// it idempotently creates a stack trace.
//
// This means that a new stack trace will not be created if there is already a
// stack trace present in the error. This helps in avoiding the creation of
// redundant stack traces, making error handling more efficient.
func Wrap(cause error, msg string, args ...interface{}) error {
	causeStackTracer := new(StackTracer)
	if errors.As(cause, causeStackTracer) {
		// If our function's cause has generated a stack trace and it is a sub-stack of our function,
		// as indicated by matching the prefix of the current program counter stack,
		// then we should enhance the error message only and avoid appending a duplicate stack trace.
		if ancestorOfCause(callers(1), (*causeStackTracer).StackTrace()) {
			return errors.WithMessagef(cause, msg, args...) // no stack - no pop
		}
	}

	// Otherwise, since a stack trace representing our function is not available, let's generate one and add it.
	// Return the result of wrapping the error cause with the given message and arguments,
	// with a new stack trace added at the top of the existing ones.
	return PopStack(errors.Wrapf(cause, msg, args...))
}

// @TODO
// ancestorOfCause returns true if the caller looks to be an ancestor of the given stack
// trace. We check this by seeing whether our stack prefix-matches the cause stack, which
// should imply the error was generated directly from our goroutine.
func ancestorOfCause(ourStack []uintptr, causeStack errors.StackTrace) bool {
	// Stack traces are ordered such that the deepest frame is first. We'll want to check
	// for prefix matching in reverse.
	//
	// As an example, imagine we have a prefix-matching stack for ourselves:
	// [
	//   "github.com/onsi/ginkgo/internal/leafnodes.(*runner).runSync",
	//   "github.com/incident-io/core/server/pkg/errors_test.TestSuite",
	//   "testing.tRunner",
	//   "runtime.goexit"
	// ]
	//
	// We'll want to compare this against an error cause that will have happened further
	// down the stack. An example stack trace from such an error might be:
	// [
	//   "github.com/incident-io/core/server/pkg/errors.New",
	//   "github.com/incident-io/core/server/pkg/errors_test.glob..func1.2.2.2.1",,
	//   "github.com/onsi/ginkgo/internal/leafnodes.(*runner).runSync",
	//   "github.com/incident-io/core/server/pkg/errors_test.TestSuite",
	//   "testing.tRunner",
	//   "runtime.goexit"
	// ]
	//
	// They prefix match, but we'll have to handle the match carefully as we need to match
	// from back to forward.

	// We can't possibly prefix match if our stack is larger than the cause stack.
	if len(ourStack) > len(causeStack) {
		return false
	}

	// We know the sizes are compatible, so compare program counters from back to front.
	for idx := 0; idx < len(ourStack); idx++ {
		if ourStack[len(ourStack)-1] != (uintptr)(causeStack[len(causeStack)-1]) {
			return false
		}
	}

	// All comparisons checked out, these stacks match
	return true
}

func callers(skip int) []uintptr {
	pc := make([]uintptr, 32)        // assume we'll have at most 32 frames
	n := runtime.Callers(skip+3, pc) // capture those frames, skipping runtime.Callers, ourself and the calling function

	return pc[:n] // return everything that we captured
}

// RecoverPanic turns a panic into an error, adjusting the stacktrace so it originates at
// the line that caused it.
//
// Example:
//
//	func Do() (err error) {
//	  defer func() {
//	    errors.RecoverPanic(recover(), &err)
//	  }()
//	}
func RecoverPanic(r interface{}, errPtr *error) {
	var err error
	if r != nil {
		if panicErr, ok := r.(error); ok {
			err = errors.Wrap(panicErr, "caught panic")
		} else {
			err = errors.New(fmt.Sprintf("caught panic: %v", r))
		}
	}

	if err != nil {
		// Pop twice: once for the errors package, then again for the defer function we must
		// run this under. We want the stacktrace to originate at the source of the panic, not
		// in the infrastructure that catches it.
		err = PopStack(err) // errors.go
		err = PopStack(err) // defer

		*errPtr = err
	}
}

// PopStack removes the top of the stack from an errors stack trace.
func PopStack(err error) error {
	if err == nil {
		return err
	}

	// We want to remove us, the internal/errors.New function, from the error stack we just
	// produced. There's no official way of reaching into the error and adjusting this, as
	// the stack is stored as a private field on an unexported struct.
	//
	// This does some unsafe badness to adjust that field, which should not be repeated
	// anywhere else.
	stackField := reflect.ValueOf(err).Elem().FieldByName("stack")
	if stackField.IsZero() {
		return err
	}
	stackFieldPtr := (**[]uintptr)(unsafe.Pointer(stackField.UnsafeAddr()))

	// Remove the first of the frames, dropping 'us' from the error stack trace.
	frames := (**stackFieldPtr)[1:]

	// Assign to the internal stack field
	*stackFieldPtr = &frames

	return err
}
