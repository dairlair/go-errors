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

// The function returns 'true' if the calling function is an ancestor of the error stack trace.
//
// Determines whether the calling function is an ancestor of the provided stack trace.
// The function achieves this by comparing the prefix of its own stack with the
// stack of the cause, which suggests that the error was generated directly
// from the calling goroutine. The function takes in an argument
// 'ourStack', which is the stack trace of the
// calling function, and 'causeStack', which is the stack trace of the error that needs to be checked.
func ancestorOfCause(ourStack []uintptr, causeStack errors.StackTrace) bool {
	// stack traces are organized in reverse order, with the deepest frame appearing first. Therefore, when checking for prefix matches, it is necessary to perform the comparison in reverse.
	//
	// E.g.:, consider the scenario where there exists a stack trace that
	// matches the prefix of the function. This can be illustrated by way of an example.:
	// [
	//   "github.com/onsi/ginkgo/internal/leafnodes.(*runner).runSync",
	//   "github.com/dairlair/go-errors/go_errors_suite_test.TestSuite",
	//   "testing.tRunner",
	//   "runtime.goexit"
	// ]
	//
	// It may be necessary to compare the function's own stack trace against an error
	// cause that has occurred further down the stack. To demonstrate this, an example stack trace from such an error is provided.
	// [
	//   "github.com/incident-io/core/server/pkg/errors.New",
	//   "github.com/incident-io/core/server/pkg/errors_test.glob..func1.2.2.2.1",,
	//   "github.com/onsi/ginkgo/internal/leafnodes.(*runner).runSync",
	//   "github.com/incident-io/core/server/pkg/errors_test.TestSuite",
	//   "testing.tRunner",
	//   "runtime.goexit"
	// ]
	//
	// A prefix match has been identified between two elements that need to be compared.
	// However, it is important to handle the match with caution since the comparison
	// should be performed from the back of the stack trace to the front, as we mentioned above,

	// it is impossible to find a prefix match if the stack trace of the calling function
	// is longer than the stack trace of the error cause.
	// Therefore, if the length of the calling function's stack trace 'ourStack'
	// is greater than the stack trace of the error cause 'causeStack',
	// the function we return false.
	if len(ourStack) > len(causeStack) {
		return false
	}

	// Sizes of the elements being compared are compatible, it is safe to compare
	// program counters from back to front.
	for idx := 0; idx < len(ourStack); idx++ {
		if ourStack[len(ourStack)-1] != (uintptr)(causeStack[len(causeStack)-1]) {
			return false
		}
	}

	// Stacks are equal, Viva la igualdad!
	return true
}

func callers(skip int) []uintptr {
	pc := make([]uintptr, 32)        // expect a maximum of 32 levels of function call hierarchy
	n := runtime.Callers(skip+3, pc) // capture those frames, skipping runtime.Callers, ourself and the calling function

	return pc[:n] // return captured frames
}

// RecoverPanic  designed to transform a panic event into an error.
//
// Additionally, the function modifies the stack trace of the error such
// that it appears to have originated from the specific line of code that triggered the panic event.
//
// E.G.:
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
		// Two pop operations are necessary within the function in order to remove the relevant stack frames.
		// The first pop is needed to remove the 'errors' package,
		// while the second pop is required to remove the defer function that encapsulates
		// the error handling infrastructure.
		// The goal of these 'pop' operations is to adjust the stack trace so that it originates
		// from the line of code that triggered the panic event, rather than the error handling code.
		err = PopStack(err) // errors.go
		err = PopStack(err) // defer

		*errPtr = err
	}
}

// PopStack used to remove the top element from a stack trace.
func PopStack(err error) error {
	if err == nil {
		return err
	}

	// We need to remove the 'errors.New' function from a newly created error stack. However,
	// there is no public method for modifying the error stack, as it is stored as a
	// private field within an unexported struct.
	//
	// To solve this problem, the function employs an unsafe operation to modify
	// the stack field, which is not recommended and should not be replicated elsewhere in the program.
	stackField := reflect.ValueOf(err).Elem().FieldByName("stack")
	if stackField.IsZero() {
		return err
	}
	stackFieldPtr := (**[]uintptr)(unsafe.Pointer(stackField.UnsafeAddr()))

	// Remove the first frame from a stack trace, effectively eliminating the element associated with 'us' from the error stack.
	frames := (**stackFieldPtr)[1:]

	// Assign to the internal stack field
	*stackFieldPtr = &frames

	return err
}
