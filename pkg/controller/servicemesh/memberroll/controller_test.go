package memberroll

import (
	"context"
	"fmt"
	"testing"
	"time"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/maistra/istio-operator/pkg/apis/maistra/status"
	maistrav1 "github.com/maistra/istio-operator/pkg/apis/maistra/v1"
	maistrav2 "github.com/maistra/istio-operator/pkg/apis/maistra/v2"
	"github.com/maistra/istio-operator/pkg/controller/common"
	"github.com/maistra/istio-operator/pkg/controller/common/test"
	"github.com/maistra/istio-operator/pkg/controller/common/test/assert"
)

const (
	memberRollName        = "default"
	memberRollUID         = types.UID("1111")
	appNamespace          = "app-namespace"
	appNamespace2         = "app-namespace-2"
	controlPlaneName      = "my-mesh"
	controlPlaneNamespace = "cp-namespace"
	controlPlaneUID       = types.UID("2222")

	memberUID = types.UID("3333")

	operatorVersion1_1     = "1.1.0"
	operatorVersionDefault = operatorVersion1_1
)

var (
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      memberRollName,
			Namespace: controlPlaneNamespace,
		},
	}

	oneMinuteAgo = meta.NewTime(time.Now().Add(-time.Minute))
)

func init() {
	logf.SetLogger(zap.New(zap.UseDevMode(true)))
}

func TestReconcileAddsFinalizer(t *testing.T) {
	roll := newDefaultMemberRoll()
	roll.Finalizers = []string{}

	cl, _, r, _ := createClientAndReconciler(roll)

	assertReconcileSucceeds(r, t)

	updatedRoll := test.GetUpdatedObject(ctx, cl, roll.ObjectMeta, &maistrav1.ServiceMeshMemberRoll{}).(*maistrav1.ServiceMeshMemberRoll)
	assert.DeepEquals(updatedRoll.GetFinalizers(), []string{common.FinalizerName}, "Unexpected finalizers in SMM", t)
}

func TestReconcileFailsIfItCannotAddFinalizer(t *testing.T) {
	roll := newDefaultMemberRoll()
	roll.Finalizers = []string{}

	_, tracker, r, _ := createClientAndReconciler(roll)
	tracker.AddReactor("update", "servicemeshmemberrolls", test.ClientFails())
	assertReconcileFails(r, t)
}

func TestReconcileDoesNothingIfMemberRollIsDeletedAndHasNoFinalizers(t *testing.T) {
	roll := newDefaultMemberRoll()
	roll.DeletionTimestamp = &oneMinuteAgo
	roll.Finalizers = nil

	_, tracker, r, _ := createClientAndReconciler(roll)

	assertReconcileSucceeds(r, t)
	test.AssertNumberOfWriteActions(t, tracker.Actions(), 0)
}

func TestReconcileDoesNothingWhenMemberRollIsNotFound(t *testing.T) {
	_, tracker, r, _ := createClientAndReconciler()
	assertReconcileSucceeds(r, t)
	test.AssertNumberOfWriteActions(t, tracker.Actions(), 0)
}

func TestReconcileFailsWhenGetMemberRollFails(t *testing.T) {
	_, tracker, r, _ := createClientAndReconciler()
	tracker.AddReactor("get", "servicemeshmemberrolls", test.ClientFails())
	assertReconcileFails(r, t)
	test.AssertNumberOfWriteActions(t, tracker.Actions(), 0)
}

func TestReconcileFailsWhenListControlPlanesFails(t *testing.T) {
	roll := newDefaultMemberRoll()
	_, tracker, r, _ := createClientAndReconciler(roll)
	tracker.AddReactor("list", "servicemeshcontrolplanes", test.ClientFails())

	assertReconcileFails(r, t)
	test.AssertNumberOfWriteActions(t, tracker.Actions(), 0)
}

func TestReconcileDoesNothingIfControlPlaneMissing(t *testing.T) {
	roll := newDefaultMemberRoll()
	cl, tracker, r, _ := createClientAndReconciler(roll)

	assertReconcileSucceeds(r, t)
	test.AssertNumberOfWriteActions(t, tracker.Actions(), 1)

	updatedRoll := test.GetUpdatedObject(ctx, cl, roll.ObjectMeta, &maistrav1.ServiceMeshMemberRoll{}).(*maistrav1.ServiceMeshMemberRoll)
	assertConditions(updatedRoll, []maistrav1.ServiceMeshMemberRollCondition{
		{
			Type:    maistrav1.ConditionTypeMemberRollReady,
			Status:  core.ConditionFalse,
			Reason:  maistrav1.ConditionReasonSMCPMissing,
			Message: "No ServiceMeshControlPlane exists in the namespace",
		},
	}, t)
}

func TestReconcileDoesNothingIfMultipleControlPlanesFound(t *testing.T) {
	roll := newDefaultMemberRoll()
	controlPlane1 := newControlPlane()
	controlPlane2 := newControlPlane()
	controlPlane2.Name = "my-mesh-2"
	cl, tracker, r, _ := createClientAndReconciler(roll, controlPlane1, controlPlane2)
	assertReconcileSucceeds(r, t)
	test.AssertNumberOfWriteActions(t, tracker.Actions(), 1)

	updatedRoll := test.GetUpdatedObject(ctx, cl, roll.ObjectMeta, &maistrav1.ServiceMeshMemberRoll{}).(*maistrav1.ServiceMeshMemberRoll)
	assertConditions(updatedRoll, []maistrav1.ServiceMeshMemberRollCondition{
		{
			Type:    maistrav1.ConditionTypeMemberRollReady,
			Status:  core.ConditionFalse,
			Reason:  maistrav1.ConditionReasonMultipleSMCP,
			Message: "Multiple ServiceMeshControlPlane resources exist in the namespace",
		},
	}, t)
}

func TestReconcileFailsIfListingMembersFails(t *testing.T) {
	roll := newDefaultMemberRoll()
	controlPlane := newControlPlane()
	markControlPlaneReconciled(controlPlane)

	_, tracker, r, _ := createClientAndReconciler(roll, controlPlane)
	tracker.AddReactor("list", "servicemeshmembers", test.ClientFails())

	assertReconcileFails(r, t)
	test.AssertNumberOfWriteActions(t, tracker.Actions(), 0)
}

func TestReconcileCreatesMember(t *testing.T) {
	cases := []struct {
		name                   string
		reactor                clienttesting.Reactor
		existingObjects        []runtime.Object
		specMembers            []string
		expectMembersCreated   []string
		expectedStatusMembers  []string
		expectedPendingMembers []string
	}{
		{
			name:                   "namespace-exists",
			existingObjects:        []runtime.Object{newNamespace(appNamespace)},
			specMembers:            []string{appNamespace},
			expectedStatusMembers:  []string{appNamespace},
			expectedPendingMembers: []string{appNamespace},
			expectMembersCreated:   []string{appNamespace},
		},
		{
			name:                   "namespace-not-exists",
			reactor:                test.On("create", "servicemeshmembers", test.ClientReturnsNotFound(maistrav1.APIGroup, "ServiceMeshMember", common.MemberName)),
			specMembers:            []string{appNamespace},
			expectedStatusMembers:  []string{appNamespace},
			expectedPendingMembers: []string{appNamespace},
			expectMembersCreated:   []string{},
		},
		{
			name:                   "multiple-members",
			existingObjects:        []runtime.Object{newNamespace(appNamespace), newNamespace(appNamespace2)},
			specMembers:            []string{appNamespace, appNamespace2},
			expectedStatusMembers:  []string{appNamespace, appNamespace2},
			expectedPendingMembers: []string{appNamespace, appNamespace2},
			expectMembersCreated:   []string{appNamespace, appNamespace2},
		},
		{
			name:                   "control-plane-as-member",
			existingObjects:        []runtime.Object{newNamespace(controlPlaneNamespace)},
			specMembers:            []string{controlPlaneNamespace},
			expectedStatusMembers:  []string{},
			expectedPendingMembers: []string{},
			expectMembersCreated:   []string{},
		},
		{
			name:                   "member-exists",
			existingObjects:        []runtime.Object{newNamespace(appNamespace), newMember()},
			specMembers:            []string{appNamespace},
			expectedStatusMembers:  []string{appNamespace},
			expectedPendingMembers: []string{appNamespace},
			expectMembersCreated:   []string{},
		},
		{
			name: "member-exists-but-points-to-different-control-plane",
			existingObjects: []runtime.Object{
				newNamespace(appNamespace),
				markMemberReconciled(
					newMemberWithRef("other-mesh", "other-mesh-namespace"),
					1, 1, 1, operatorVersionDefault),
			},
			specMembers:            []string{appNamespace},
			expectedStatusMembers:  []string{appNamespace},
			expectedPendingMembers: []string{appNamespace},
			expectMembersCreated:   []string{},
		},
		// TODO: add namespace that contains a different control plane as a member
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roll := newDefaultMemberRoll()
			roll.Spec.Members = tc.specMembers
			controlPlane := newControlPlane()
			markControlPlaneReconciled(controlPlane)

			objects := []runtime.Object{roll, controlPlane}
			objects = append(objects, tc.existingObjects...)
			cl, _, r, _ := createClientAndReconciler(objects...)

			assertReconcileSucceeds(r, t)

			for _, ns := range tc.expectMembersCreated {
				assertMemberCreated(cl, t, ns, controlPlaneName, controlPlaneNamespace)
			}

			updatedRoll := test.GetUpdatedObject(ctx, cl, roll.ObjectMeta, &maistrav1.ServiceMeshMemberRoll{}).(*maistrav1.ServiceMeshMemberRoll)
			assertStatusMembers(updatedRoll, tc.expectedStatusMembers, tc.expectedPendingMembers, []string{}, []string{}, t)
			assert.Equals(updatedRoll.Status.ServiceMeshGeneration, controlPlane.Status.ObservedGeneration,
				"Unexpected Status.ServiceMeshGeneration in SMMR", t)
			assert.Equals(updatedRoll.Status.ServiceMeshReconciledVersion, controlPlane.Status.GetReconciledVersion(),
				"Unexpected Status.ServiceMeshReconciledVersion in SMMR", t)
		})
	}
}

func TestReconcileCreatesMemberWhenAppNamespaceIsCreated(t *testing.T) {
	roll := newDefaultMemberRoll()
	roll.Spec.Members = []string{appNamespace}
	roll.ObjectMeta.Generation = 2
	roll.Status.ObservedGeneration = 2 // NOTE: generation 2 of the member roll has already been reconciled
	controlPlane := markControlPlaneReconciled(newControlPlane())
	roll.Status.ServiceMeshGeneration = controlPlane.Status.ObservedGeneration
	namespace := newNamespace(appNamespace)

	cl, _, r, kialiReconciler := createClientAndReconciler(roll, controlPlane, namespace)

	assertReconcileSucceeds(r, t)

	assertMemberCreated(cl, t, appNamespace, controlPlaneName, controlPlaneNamespace)

	updatedRoll := test.GetUpdatedObject(ctx, cl, roll.ObjectMeta, &maistrav1.ServiceMeshMemberRoll{}).(*maistrav1.ServiceMeshMemberRoll)
	assertStatusMembers(updatedRoll, []string{appNamespace}, []string{appNamespace}, []string{}, []string{}, t)
	assert.Equals(updatedRoll.Status.ServiceMeshGeneration, controlPlane.Status.ObservedGeneration, "Unexpected Status.ServiceMeshGeneration in SMMR", t)

	kialiReconciler.assertInvokedWith(t, appNamespace)
}

func TestReconcileDeletesMemberWhenRemovedFromSpecMembers(t *testing.T) {
	cases := []struct {
		name                       string
		reactor                    clienttesting.Reactor
		initMemberFunc             func(*maistrav1.ServiceMeshMember)
		memberNamespace            string
		expectedStatusMembers      []string
		expectedPendingMembers     []string
		expectedConfiguredMembers  []string
		expectedTerminatingMembers []string
		expectMemberDeleted        bool
	}{
		{
			name:            "created-by-controller",
			memberNamespace: appNamespace,
			initMemberFunc: func(member *maistrav1.ServiceMeshMember) {
				if member.Annotations == nil {
					member.Annotations = map[string]string{}
				}
				member.Annotations[common.CreatedByKey] = controllerName
			},
			expectMemberDeleted:        true,
			expectedStatusMembers:      []string{appNamespace},
			expectedConfiguredMembers:  []string{appNamespace},
			expectedPendingMembers:     []string{},
			expectedTerminatingMembers: []string{},
		},
		{
			name:                       "created-manually",
			memberNamespace:            appNamespace,
			expectMemberDeleted:        false,
			expectedStatusMembers:      []string{appNamespace},
			expectedConfiguredMembers:  []string{appNamespace},
			expectedPendingMembers:     []string{},
			expectedTerminatingMembers: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roll := newDefaultMemberRoll()
			controlPlane := newControlPlane()
			markControlPlaneReconciled(controlPlane)

			member := newMember()
			markMemberReconciled(member, 1, 1, controlPlane.Status.ObservedGeneration, controlPlane.Status.OperatorVersion)
			if tc.initMemberFunc != nil {
				tc.initMemberFunc(member)
			}

			cl, _, r, _ := createClientAndReconciler(member, roll, controlPlane)

			assertReconcileSucceeds(r, t)

			err := cl.Get(ctx, client.ObjectKey{Name: common.MemberName, Namespace: tc.memberNamespace}, &maistrav1.ServiceMeshMember{})
			memberExists := true
			if err != nil {
				if errors.IsNotFound(err) {
					memberExists = false
				} else {
					t.Fatalf("Unexpected error %v", err)
				}
			}

			if tc.expectMemberDeleted {
				if memberExists {
					t.Fatalf("Expected controller to delete ServiceMeshMember, but it didn't")
				}
			} else {
				if !memberExists {
					t.Fatalf("Expected controller to preserve ServiceMeshMember, but it has deleted it")
				}
			}

			updatedRoll := test.GetUpdatedObject(ctx, cl, roll.ObjectMeta, &maistrav1.ServiceMeshMemberRoll{}).(*maistrav1.ServiceMeshMemberRoll)
			assertStatusMembers(updatedRoll, tc.expectedStatusMembers, tc.expectedPendingMembers, tc.expectedConfiguredMembers, tc.expectedTerminatingMembers, t)
			assert.Equals(updatedRoll.Status.ServiceMeshGeneration, controlPlane.Status.ObservedGeneration,
				"Unexpected Status.ServiceMeshGeneration in SMMR", t)
			assert.Equals(updatedRoll.Status.ServiceMeshReconciledVersion, controlPlane.Status.GetReconciledVersion(),
				"Unexpected Status.ServiceMeshReconciledVersion in SMMR", t)
		})
	}
}

func TestReconcileFailsIfMemberRollUpdateFails(t *testing.T) {
	roll := newMemberRoll(2)
	roll.Spec.Members = []string{appNamespace}
	controlPlane := markControlPlaneReconciled(newControlPlane())
	namespace := newNamespace(appNamespace)

	_, tracker, r, kialiReconciler := createClientAndReconciler(roll, controlPlane, namespace)
	tracker.AddReactor("patch", "servicemeshmemberrolls", test.ClientFails())

	assertReconcileFails(r, t)

	kialiReconciler.assertInvokedWith(t, appNamespace)
}

func TestReconcileFailsIfKialiReconcileFails(t *testing.T) {
	roll := newMemberRoll(2)
	roll.Spec.Members = []string{appNamespace}
	controlPlane := markControlPlaneReconciled(newControlPlane())
	namespace := newNamespace(appNamespace)

	_, _, r, kialiReconciler := createClientAndReconciler(roll, controlPlane, namespace)
	kialiReconciler.errorToReturn = fmt.Errorf("error")

	assertReconcileFails(r, t)

	kialiReconciler.assertInvokedWith(t, appNamespace)
}

func TestReconcileUpdatesMembersInStatusWhenMemberIsDeleted(t *testing.T) {
	roll := newMemberRoll(1)
	roll.Spec.Members = []string{appNamespace}
	roll.Status.Members = []string{appNamespace}
	roll.Status.ConfiguredMembers = []string{appNamespace}

	controlPlane := markControlPlaneReconciled(newControlPlane())
	namespace := newNamespace(appNamespace)

	cl, tracker, r, kialiReconciler := createClientAndReconciler(roll, controlPlane, namespace)
	tracker.AddReactor("create", "servicemeshmembers", test.ClientReturnsNotFound(maistrav1.APIGroup, "ServiceMeshMember", common.MemberName))

	assertReconcileSucceeds(r, t)

	updatedRoll := test.GetUpdatedObject(ctx, cl, roll.ObjectMeta, &maistrav1.ServiceMeshMemberRoll{}).(*maistrav1.ServiceMeshMemberRoll)
	assertStatusMembers(updatedRoll, []string{appNamespace}, []string{appNamespace}, []string{}, []string{}, t)
	assert.Equals(updatedRoll.Status.ServiceMeshGeneration, controlPlane.Status.ObservedGeneration, "Unexpected Status.ServiceMeshGeneration in SMMR", t)
	kialiReconciler.assertInvokedWith(t, appNamespace)
}

func TestReconcileClearsConfigureMembersWhenSMCPDeleted(t *testing.T) {
	roll := newMemberRoll(1)
	roll.Spec.Members = []string{appNamespace}
	roll.Status.Members = []string{appNamespace}
	roll.Status.PendingMembers = []string{}
	roll.Status.ConfiguredMembers = []string{appNamespace}
	roll.Status.TerminatingMembers = []string{}
	roll.Status.MemberStatuses = []maistrav1.ServiceMeshMemberStatusSummary{
		{
			Namespace: appNamespace,
			Conditions: []maistrav1.ServiceMeshMemberCondition{
				{
					Type:   maistrav1.ConditionTypeMemberReconciled,
					Status: core.ConditionTrue,
				},
			},
		},
	}

	namespace := newNamespace(appNamespace)

	cl, _, r, _ := createClientAndReconciler(roll, namespace)

	assertReconcileSucceeds(r, t)

	updatedRoll := test.GetUpdatedObject(ctx, cl, roll.ObjectMeta, &maistrav1.ServiceMeshMemberRoll{}).(*maistrav1.ServiceMeshMemberRoll)
	assertStatusMembers(updatedRoll,
		[]string{appNamespace}, // members
		[]string{appNamespace}, // pending
		[]string{},             // configured
		[]string{},             // terminating
		t)
}

func TestReconcileRemovesFinalizerFromMemberRoll(t *testing.T) {
	roll := newDefaultMemberRoll()
	roll.DeletionTimestamp = &oneMinuteAgo

	cl, _, r, _ := createClientAndReconciler(roll)

	assertReconcileSucceeds(r, t)

	updatedRoll := test.GetUpdatedObject(ctx, cl, roll.ObjectMeta, &maistrav1.ServiceMeshMemberRoll{}).(*maistrav1.ServiceMeshMemberRoll)
	assert.StringArrayEmpty(updatedRoll.Finalizers, "Expected finalizers list in SMMR to be empty, but it wasn't", t)
}

func TestReconcileHandlesDeletionProperly(t *testing.T) {
	cases := []struct {
		name                      string
		specMembers               []string
		configuredMembers         []string
		expectedRemovedNamespaces []string
		noKiali                   bool
	}{
		{
			name:                      "normal-deletion",
			specMembers:               []string{appNamespace},
			configuredMembers:         []string{appNamespace},
			expectedRemovedNamespaces: []string{appNamespace},
		},
		{
			name:                      "normal-deletion-no-kiali",
			specMembers:               []string{appNamespace},
			configuredMembers:         []string{appNamespace},
			expectedRemovedNamespaces: []string{appNamespace},
			noKiali:                   true,
		},
		{
			name:        "ns-removed-from-members-list-and-smmr-deleted-immediately",
			specMembers: []string{}, // appNamespace was removed, but then the SMMR was deleted immediately.
			// The controller is reconciling both actions at once.
			configuredMembers:         []string{appNamespace},
			expectedRemovedNamespaces: []string{appNamespace},
		},
		// TODO: add a member, it gets configured by namespace reconciler, but then the SMMR update fails
		//  (configuredMembers doesn't include the namespace). Then the SMMR is deleted. Does the namespace get cleaned up?
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controlPlane := newControlPlane()
			if tc.noKiali {
				controlPlane.Status.AppliedSpec.Addons = nil
			}
			markControlPlaneReconciled(newControlPlane())

			roll := newDefaultMemberRoll()
			roll.Spec.Members = tc.specMembers
			roll.Status.ConfiguredMembers = tc.configuredMembers
			if !tc.noKiali {
				if roll.Status.Annotations == nil {
					roll.Status.Annotations = map[string]string{}
				}
				roll.Status.Annotations[statusAnnotationKialiName] = "kiali"
			}
			roll.DeletionTimestamp = &oneMinuteAgo

			initObjects := []runtime.Object{roll, controlPlane}
			for _, ns := range tc.configuredMembers {
				initObjects = append(initObjects, &core.Namespace{
					ObjectMeta: meta.ObjectMeta{
						Name: ns,
						Labels: map[string]string{
							common.MemberOfKey: controlPlaneNamespace,
						},
					},
				})
			}

			cl, _, r, kialiReconciler := createClientAndReconciler(initObjects...)

			assertReconcileSucceeds(r, t)

			updatedRoll := test.GetUpdatedObject(ctx, cl, roll.ObjectMeta, &maistrav1.ServiceMeshMemberRoll{}).(*maistrav1.ServiceMeshMemberRoll)
			assert.StringArrayEmpty(updatedRoll.Finalizers, "Expected finalizers list in SMMR to be empty, but it wasn't", t)

			if !tc.noKiali {
				kialiReconciler.assertInvokedWith(t /* no namespaces */)
			}
		})
	}
}

func TestClientReturnsErrorWhenRemovingFinalizer(t *testing.T) {
	cases := []struct {
		name                 string
		reactor              clienttesting.Reactor
		successExpected      bool
		expectedWriteActions int
	}{
		{
			name:                 "get-memberroll-returns-notfound",
			reactor:              test.On("get", "servicemeshmemberrolls", test.ClientReturnsNotFound(maistrav1.APIGroup, "ServiceMeshMemberRoll", memberRollName)),
			successExpected:      true,
			expectedWriteActions: 0,
		},
		{
			name:                 "get-memberroll-fails",
			reactor:              test.On("get", "servicemeshmemberrolls", test.ClientFails()),
			successExpected:      false,
			expectedWriteActions: 0,
		},
		{
			name:                 "update-memberroll-returns-notfound",
			reactor:              test.On("update", "servicemeshmemberrolls", test.ClientReturnsNotFound(maistrav1.APIGroup, "ServiceMeshMemberRoll", memberRollName)),
			successExpected:      true,
			expectedWriteActions: 1,
		},
		{
			name:                 "update-memberroll-fails",
			reactor:              test.On("update", "servicemeshmemberrolls", test.ClientFails()),
			successExpected:      false,
			expectedWriteActions: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controlPlane := markControlPlaneReconciled(newControlPlane())
			roll := newDefaultMemberRoll()
			roll.DeletionTimestamp = &oneMinuteAgo

			_, tracker, r, _ := createClientAndReconciler(roll, controlPlane)
			tracker.AddReaction(tc.reactor)

			if tc.successExpected {
				assertReconcileSucceeds(r, t)
			} else {
				assertReconcileFails(r, t)
			}
			test.AssertNumberOfWriteActions(t, tracker.Actions(), tc.expectedWriteActions)
		})
	}
}

func newMember() *maistrav1.ServiceMeshMember {
	return newMemberWithRef(controlPlaneName, controlPlaneNamespace)
}

func newMemberWithRef(controlPlaneName, controlPlaneNamespace string) *maistrav1.ServiceMeshMember {
	return &maistrav1.ServiceMeshMember{
		ObjectMeta: meta.ObjectMeta{
			Name:       common.MemberName,
			Namespace:  appNamespace,
			Finalizers: []string{common.FinalizerName},
		},
		Spec: maistrav1.ServiceMeshMemberSpec{
			ControlPlaneRef: maistrav1.ServiceMeshControlPlaneRef{
				Name:      controlPlaneName,
				Namespace: controlPlaneNamespace,
			},
		},
	}
}

func markMemberReconciled(member *maistrav1.ServiceMeshMember, generation, observedGeneration, observedMeshGeneration int64,
	operatorVersion string,
) *maistrav1.ServiceMeshMember {
	member.Finalizers = []string{common.FinalizerName}
	member.Generation = generation
	member.UID = memberUID
	member.Status.ObservedGeneration = observedGeneration
	member.Status.ServiceMeshGeneration = observedMeshGeneration
	member.Status.ServiceMeshReconciledVersion = status.ComposeReconciledVersion(operatorVersion, observedMeshGeneration)

	member.Status.SetCondition(maistrav1.ServiceMeshMemberCondition{
		Type:    maistrav1.ConditionTypeMemberReconciled,
		Status:  common.BoolToConditionStatus(true),
		Reason:  "",
		Message: "",
	})
	member.Status.SetCondition(maistrav1.ServiceMeshMemberCondition{
		Type:    maistrav1.ConditionTypeMemberReady,
		Status:  common.BoolToConditionStatus(true),
		Reason:  "",
		Message: "",
	})
	return member
}

func createClientAndReconciler(clientObjects ...runtime.Object) (client.Client, *test.EnhancedTracker, *MemberRollReconciler, *fakeKialiReconciler) {
	cl, enhancedTracker := test.CreateClient(clientObjects...)

	fakeEventRecorder := &record.FakeRecorder{}
	kialiReconciler := &fakeKialiReconciler{}

	r := newReconciler(cl, scheme.Scheme, fakeEventRecorder, kialiReconciler)
	return cl, enhancedTracker, r, kialiReconciler
}

func assertReconcileSucceeds(r *MemberRollReconciler, t *testing.T) {
	res, err := r.Reconcile(request)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if res.Requeue {
		t.Error("Reconcile requeued the request, but it shouldn't have")
	}
}

func assertReconcileFails(r *MemberRollReconciler, t *testing.T) {
	_, err := r.Reconcile(request)
	if err == nil {
		t.Fatal("Expected reconcile to fail, but it didn't")
	}
}

func newDefaultMemberRoll() *maistrav1.ServiceMeshMemberRoll {
	return newMemberRoll(1)
}

func newMemberRoll(generation int64) *maistrav1.ServiceMeshMemberRoll {
	operatorVersion := operatorVersionDefault
	observedGeneration := int64(1)
	observedMeshGeneration := int64(1)
	return &maistrav1.ServiceMeshMemberRoll{
		ObjectMeta: meta.ObjectMeta{
			Name:       memberRollName,
			Namespace:  controlPlaneNamespace,
			Finalizers: []string{common.FinalizerName},
			Generation: generation,
			UID:        memberRollUID,
		},
		Spec: maistrav1.ServiceMeshMemberRollSpec{
			Members: []string{},
		},
		Status: maistrav1.ServiceMeshMemberRollStatus{
			ObservedGeneration:           observedGeneration,
			ServiceMeshGeneration:        observedMeshGeneration,
			ServiceMeshReconciledVersion: status.ComposeReconciledVersion(operatorVersion, observedMeshGeneration),
		},
	}
}

func newControlPlane() *maistrav2.ServiceMeshControlPlane {
	enabled := true
	return &maistrav2.ServiceMeshControlPlane{
		ObjectMeta: meta.ObjectMeta{
			Name:       controlPlaneName,
			Namespace:  controlPlaneNamespace,
			UID:        controlPlaneUID,
			Generation: 1,
		},
		Spec: maistrav2.ControlPlaneSpec{
			Version: "",
			Addons: &maistrav2.AddonsConfig{
				Kiali: &maistrav2.KialiAddonConfig{
					Enablement: maistrav2.Enablement{
						Enabled: &enabled,
					},
					Name: "kiali",
				},
			},
		},
	}
}

func markControlPlaneReconciled(controlPlane *maistrav2.ServiceMeshControlPlane) *maistrav2.ServiceMeshControlPlane {
	controlPlane.Status.ObservedGeneration = controlPlane.GetGeneration()
	controlPlane.Status.Conditions = []status.Condition{
		{
			Type:   status.ConditionTypeReconciled,
			Status: status.ConditionStatusTrue,
		},
	}
	controlPlane.Status.ObservedGeneration = controlPlane.GetGeneration()
	controlPlane.Status.OperatorVersion = operatorVersionDefault
	controlPlane.Spec.DeepCopyInto(&controlPlane.Status.AppliedSpec)
	return controlPlane
}

func newNamespace(name string) *core.Namespace {
	namespace := &core.Namespace{
		ObjectMeta: meta.ObjectMeta{
			Name:   name,
			Labels: map[string]string{},
		},
	}
	return namespace
}

type fakeKialiReconciler struct {
	reconcileKialiInvoked  bool
	kialiConfiguredMembers []string
	errorToReturn          error
	delegate               func(ctx context.Context, kialiCRName, kialiCRNamespace string, configuredMembers []string) error
}

func (f *fakeKialiReconciler) reconcileKiali(ctx context.Context, kialiCRName, kialiCRNamespace string, configuredMembers []string) error {
	f.reconcileKialiInvoked = true
	f.kialiConfiguredMembers = append([]string{}, configuredMembers...)
	if f.errorToReturn != nil {
		return f.errorToReturn
	}
	if f.delegate != nil {
		return f.delegate(ctx, kialiCRName, kialiCRNamespace, configuredMembers)
	}
	return nil
}

func (f *fakeKialiReconciler) assertInvokedWith(t *testing.T, namespaces ...string) {
	t.Helper()
	assert.True(f.reconcileKialiInvoked, "Expected reconcileKiali to be invoked, but it wasn't", t)
	if len(namespaces) != 0 || len(f.kialiConfiguredMembers) != 0 {
		assert.DeepEquals(f.kialiConfiguredMembers, namespaces, "reconcileKiali called with unexpected member list", t)
	}
}

func (f *fakeKialiReconciler) assertNotInvoked(t *testing.T) {
	assert.False(f.reconcileKialiInvoked, "Expected reconcileKiali not to be invoked, but it was", t)
}

func assertConditions(roll *maistrav1.ServiceMeshMemberRoll, expected []maistrav1.ServiceMeshMemberRollCondition, t *testing.T) {
	assert.DeepEquals(removeTimestamps(roll.Status.Conditions), expected, "Unexpected Status.Conditions in SMMR", t)
}

func removeTimestamps(conditions []maistrav1.ServiceMeshMemberRollCondition) []maistrav1.ServiceMeshMemberRollCondition {
	copies := make([]maistrav1.ServiceMeshMemberRollCondition, len(conditions))
	for i, cond := range conditions {
		condCopy := cond.DeepCopy()
		condCopy.LastTransitionTime = meta.Time{}
		copies[i] = *condCopy
	}
	return copies
}

func assertStatusMembers(roll *maistrav1.ServiceMeshMemberRoll, members, pending, configured, terminating []string, t *testing.T) {
	t.Helper()
	assert.DeepEquals(roll.Status.Members, members, "Unexpected Status.Members in SMMR", t)
	assert.DeepEquals(roll.Status.PendingMembers, pending, "Unexpected Status.PendingMembers in SMMR", t)
	assert.DeepEquals(roll.Status.ConfiguredMembers, configured, "Unexpected Status.ConfiguredMembers in SMMR", t)
	assert.DeepEquals(roll.Status.TerminatingMembers, terminating, "Unexpected Status.TerminatingMembers in SMMR", t)
	for _, member := range members {
		found := false
		for _, memberStatus := range roll.Status.MemberStatuses {
			if memberStatus.Namespace == member {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("No entry for namespace %s found in ServiceMeshMemberRoll.Status.MemberStatuses: %v", member, roll.Status.MemberStatuses)
		}
	}
}

func assertMemberCreated(cl client.Client, t *testing.T, namespace string, controlPlaneName string, controlPlaneNamespace string) {
	t.Helper()
	member := maistrav1.ServiceMeshMember{}
	err := cl.Get(ctx, client.ObjectKey{Name: common.MemberName, Namespace: namespace}, &member)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	assert.DeepEquals(member.Spec.ControlPlaneRef, maistrav1.ServiceMeshControlPlaneRef{
		Name:      controlPlaneName,
		Namespace: controlPlaneNamespace,
	}, "Unexpected controlPlaneRef in ServiceMeshMember", t)
	assert.Equals(member.ObjectMeta.Annotations[common.CreatedByKey], controllerName, "Unexpected created-by annotation  in ServiceMeshMember", t)
}
