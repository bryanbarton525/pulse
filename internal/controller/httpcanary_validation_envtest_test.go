package controller

import (
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
)

var _ = Describe("HttpCanary schema validation", func() {
	It("rejects an interval above one hour", func() {
		canary := &canaryv1alpha1.HttpCanary{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "interval-too-large-",
				Namespace:    "default",
			},
			Spec: canaryv1alpha1.HttpCanarySpec{
				URL:            "http://example.invalid",
				Interval:       3601,
				ExpectedStatus: 200,
			},
		}

		err := k8sClient.Create(ctx, canary)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected Invalid, got %v", err)
	})
})
