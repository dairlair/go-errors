package errors_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGoErrors(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "GoErrors Suite")
}
