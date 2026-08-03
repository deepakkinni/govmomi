// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package simulator

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/cns"
	cnstypes "github.com/vmware/govmomi/cns/types"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/task"
	vim25types "github.com/vmware/govmomi/vim25/types"
	"github.com/vmware/govmomi/vslm"
)

const (
	testLabel = "testLabel"
	testValue = "testValue"
)

func TestUnregisterVolumeEx(t *testing.T) {
	ctx := context.Background()

	model := simulator.VPX()
	defer model.Remove()

	if err := model.Create(); err != nil {
		t.Fatal(err)
	}

	s := model.Service.NewServer()
	defer s.Close()

	model.Service.RegisterSDK(New())

	c, err := govmomi.NewClient(ctx, s.URL, true)
	if err != nil {
		t.Fatal(err)
	}

	cnsClient, err := cns.NewClient(ctx, c.Client)
	if err != nil {
		t.Fatal(err)
	}

	datastore := model.Map().Any("Datastore").(*simulator.Datastore)
	var capacityInMb int64 = 1024

	// Create a volume to operate on
	createTask, err := cnsClient.CreateVolume(ctx, []cnstypes.CnsVolumeCreateSpec{
		{
			Name:       "test-unregister-ex",
			VolumeType: "TestVolumeType",
			Datastores: []vim25types.ManagedObjectReference{datastore.Self},
			BackingObjectDetails: &cnstypes.CnsBlockBackingDetails{
				CnsBackingObjectDetails: cnstypes.CnsBackingObjectDetails{
					CapacityInMb: capacityInMb,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	createTaskInfo, err := cns.GetTaskInfo(ctx, createTask)
	if err != nil {
		t.Fatal(err)
	}
	createTaskResult, err := cns.GetTaskResult(ctx, createTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if createTaskResult == nil {
		t.Fatal("empty create task result")
	}
	if createTaskResult.GetCnsVolumeOperationResult().Fault != nil {
		t.Fatalf("create volume fault: %+v", createTaskResult.GetCnsVolumeOperationResult().Fault)
	}
	volumeId := createTaskResult.GetCnsVolumeOperationResult().VolumeId

	// UnregisterVolumeEx -- removes the volume from CNS inventory in one call.
	unregExTask, err := cnsClient.UnregisterVolumeEx(ctx, []cnstypes.CnsUnregisterVolumeSpec{
		{
			VolumeId:         volumeId,
			TargetVolumeType: string(cnstypes.CnsUnregisterTargetVolumeTypeLEGACY_DISK),
		},
	})
	if err != nil {
		t.Fatalf("UnregisterVolumeEx failed: %v", err)
	}
	unregExTaskInfo, err := cns.GetTaskInfo(ctx, unregExTask)
	if err != nil {
		t.Fatalf("GetTaskInfo for UnregisterVolumeEx failed: %v", err)
	}
	unregExResult, err := cns.GetTaskResult(ctx, unregExTaskInfo)
	if err != nil {
		t.Fatalf("GetTaskResult for UnregisterVolumeEx failed: %v", err)
	}
	if unregExResult == nil {
		t.Fatal("empty UnregisterVolumeEx task result")
	}
	if unregExResult.GetCnsVolumeOperationResult().Fault != nil {
		t.Fatalf("UnregisterVolumeEx fault: %+v", unregExResult.GetCnsVolumeOperationResult().Fault)
	}
	unregVolumeResult := unregExResult.(*cnstypes.CnsUnregisterVolumeResult)
	t.Logf("UnregisterVolumeEx result: backingDiskPath=%q diskUUID=%q",
		unregVolumeResult.BackingDiskPath, unregVolumeResult.DiskUUID)

	// Verify the volume is gone from CNS
	queryResult, err := cnsClient.QueryVolume(ctx, &cnstypes.CnsQueryFilter{
		VolumeIds: []cnstypes.CnsVolumeId{volumeId},
	})
	if err != nil {
		t.Fatalf("QueryVolume after unregister failed: %v", err)
	}
	if len(queryResult.Volumes) != 0 {
		t.Fatalf("expected 0 volumes after UnregisterVolumeEx, got %d", len(queryResult.Volumes))
	}

	// Re-issuing UnregisterVolumeEx on the same (now-gone) volume must report
	// NotFound rather than success a second time; callers treat that as success.
	reUnregTask, err := cnsClient.UnregisterVolumeEx(ctx, []cnstypes.CnsUnregisterVolumeSpec{
		{
			VolumeId:         volumeId,
			TargetVolumeType: string(cnstypes.CnsUnregisterTargetVolumeTypeLEGACY_DISK),
		},
	})
	if err != nil {
		t.Fatalf("second UnregisterVolumeEx failed: %v", err)
	}
	if _, err := cns.GetTaskInfo(ctx, reUnregTask); err == nil {
		t.Fatal("expected second UnregisterVolumeEx to fault with NotFound, task succeeded")
	} else if fault := taskFault(t, err); fault == nil {
		t.Fatalf("expected a task fault, got plain error: %v", err)
	} else if _, ok := fault.(*vim25types.NotFound); !ok {
		t.Fatalf("expected NotFound fault on second UnregisterVolumeEx, got %T", fault)
	}
}

// taskFault extracts the BaseMethodFault from a task-level error returned by
// cns.GetTaskInfo, or nil if err is not a task.Error.
func taskFault(t *testing.T, err error) vim25types.BaseMethodFault {
	t.Helper()
	terr, ok := err.(task.Error)
	if !ok {
		return nil
	}
	return terr.Fault()
}

// TestQueryUnregisterFeasibility exercises the side-effect-free unregister
// precondition check: an all-feasible batch, injected blockers of each
// disposition, a mixed known/unknown volume batch, and the argument-length
// validation boundaries.
func TestQueryUnregisterFeasibility(t *testing.T) {
	ctx := context.Background()

	model := simulator.VPX()
	defer model.Remove()

	if err := model.Create(); err != nil {
		t.Fatal(err)
	}

	s := model.Service.NewServer()
	defer s.Close()

	registry := New()
	model.Service.RegisterSDK(registry)

	c, err := govmomi.NewClient(ctx, s.URL, true)
	if err != nil {
		t.Fatal(err)
	}

	cnsClient, err := cns.NewClient(ctx, c.Client)
	if err != nil {
		t.Fatal(err)
	}

	volumeManager := registry.Get(cns.CnsVolumeManagerInstance).(*CnsVolumeManager)

	datastore := model.Map().Any("Datastore").(*simulator.Datastore)
	var capacityInMb int64 = 1024

	createVolume := func(name string) cnstypes.CnsVolumeId {
		createTask, err := cnsClient.CreateVolume(ctx, []cnstypes.CnsVolumeCreateSpec{
			{
				Name:       name,
				VolumeType: "TestVolumeType",
				Datastores: []vim25types.ManagedObjectReference{datastore.Self},
				BackingObjectDetails: &cnstypes.CnsBlockBackingDetails{
					CnsBackingObjectDetails: cnstypes.CnsBackingObjectDetails{
						CapacityInMb: capacityInMb,
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		taskInfo, err := cns.GetTaskInfo(ctx, createTask)
		if err != nil {
			t.Fatal(err)
		}
		taskResult, err := cns.GetTaskResult(ctx, taskInfo)
		if err != nil {
			t.Fatal(err)
		}
		opRes := taskResult.GetCnsVolumeOperationResult()
		if opRes.Fault != nil {
			t.Fatalf("create volume %q fault: %+v", name, opRes.Fault)
		}
		return opRes.VolumeId
	}

	queryFeasibility := func(volumeIds []cnstypes.CnsVolumeId) []cnstypes.CnsUnregisterFeasibilityResult {
		queryTask, err := cnsClient.QueryUnregisterFeasibility(
			ctx, volumeIds, string(cnstypes.CnsUnregisterTargetVolumeTypeLEGACY_DISK))
		if err != nil {
			t.Fatal(err)
		}
		taskInfo, err := cns.GetTaskInfo(ctx, queryTask)
		if err != nil {
			t.Fatal(err)
		}
		resultArray, err := cns.GetTaskResultArray(ctx, taskInfo)
		if err != nil {
			t.Fatal(err)
		}
		results := make([]cnstypes.CnsUnregisterFeasibilityResult, 0, len(resultArray))
		for _, r := range resultArray {
			results = append(results, *r.(*cnstypes.CnsUnregisterFeasibilityResult))
		}
		return results
	}

	t.Run("all feasible", func(t *testing.T) {
		v1 := createVolume("feasibility-a")
		v2 := createVolume("feasibility-b")

		results := queryFeasibility([]cnstypes.CnsVolumeId{v1, v2})
		if len(results) != 2 {
			t.Fatalf("expected 2 results, got %d", len(results))
		}
		for i, want := range []cnstypes.CnsVolumeId{v1, v2} {
			if results[i].VolumeId.Id != want.Id {
				t.Fatalf("result[%d] volumeId mismatch: got %q, want %q", i, results[i].VolumeId.Id, want.Id)
			}
			if !results[i].Feasible {
				t.Fatalf("result[%d] volume=%s: expected feasible, blockers=%+v", i, want.Id, results[i].Blockers)
			}
			if len(results[i].Blockers) != 0 {
				t.Fatalf("result[%d] volume=%s: expected no blockers, got %+v", i, want.Id, results[i].Blockers)
			}
		}
	})

	t.Run("injected blockers by disposition", func(t *testing.T) {
		permanent := createVolume("feasibility-permanent")
		structural := createVolume("feasibility-structural")
		transient := createVolume("feasibility-transient")

		volumeManager.SetUnregisterBlockers(permanent, []cnstypes.CnsUnregisterBlocker{
			{
				Condition:   cnstypes.CnsUnregisterBlockerConditionLinkedClone,
				Disposition: cnstypes.CnsUnregisterBlockerDispositionPermanent,
				Detail:      "volume is a linked clone",
			},
		})
		volumeManager.SetUnregisterBlockers(structural, []cnstypes.CnsUnregisterBlocker{
			{
				Condition:   cnstypes.CnsUnregisterBlockerConditionFcdSnapshotsPresent,
				Disposition: cnstypes.CnsUnregisterBlockerDispositionStructural,
				Detail:      "1 FCD snapshot present",
			},
		})
		volumeManager.SetUnregisterBlockers(transient, []cnstypes.CnsUnregisterBlocker{
			{
				Condition:   cnstypes.CnsUnregisterBlockerConditionHostUnreachable,
				Disposition: cnstypes.CnsUnregisterBlockerDispositionTransient,
			},
		})
		t.Cleanup(func() {
			volumeManager.SetUnregisterBlockers(permanent, nil)
			volumeManager.SetUnregisterBlockers(structural, nil)
			volumeManager.SetUnregisterBlockers(transient, nil)
		})

		results := queryFeasibility([]cnstypes.CnsVolumeId{permanent, structural, transient})
		if len(results) != 3 {
			t.Fatalf("expected 3 results, got %d", len(results))
		}

		wantCondition := []string{
			cnstypes.CnsUnregisterBlockerConditionLinkedClone,
			cnstypes.CnsUnregisterBlockerConditionFcdSnapshotsPresent,
			cnstypes.CnsUnregisterBlockerConditionHostUnreachable,
		}
		wantDisposition := []string{
			cnstypes.CnsUnregisterBlockerDispositionPermanent,
			cnstypes.CnsUnregisterBlockerDispositionStructural,
			cnstypes.CnsUnregisterBlockerDispositionTransient,
		}
		for i, r := range results {
			if r.Feasible {
				t.Fatalf("result[%d]: expected infeasible", i)
			}
			if len(r.Blockers) != 1 {
				t.Fatalf("result[%d]: expected exactly 1 blocker, got %d", i, len(r.Blockers))
			}
			if r.Blockers[0].Condition != wantCondition[i] {
				t.Errorf("result[%d] condition: got %q, want %q", i, r.Blockers[0].Condition, wantCondition[i])
			}
			if r.Blockers[0].Disposition != wantDisposition[i] {
				t.Errorf("result[%d] disposition: got %q, want %q", i, r.Blockers[0].Disposition, wantDisposition[i])
			}
		}
	})

	t.Run("unknown volume reports a fault, known volumes still resolve", func(t *testing.T) {
		known := createVolume("feasibility-known")
		unknown := cnstypes.CnsVolumeId{Id: uuid.New().String()}

		results := queryFeasibility([]cnstypes.CnsVolumeId{known, unknown})
		if len(results) != 2 {
			t.Fatalf("expected 2 results, got %d", len(results))
		}
		if results[0].Fault != nil {
			t.Fatalf("known volume: unexpected fault %+v", results[0].Fault)
		}
		if !results[0].Feasible {
			t.Fatalf("known volume: expected feasible")
		}
		if results[1].Fault == nil {
			t.Fatal("unknown volume: expected a fault, got none")
		}
		if _, ok := results[1].Fault.Fault.(*vim25types.NotFound); !ok {
			t.Fatalf("unknown volume: expected NotFound fault, got %T", results[1].Fault.Fault)
		}
	})

	t.Run("empty volumeIds is rejected", func(t *testing.T) {
		queryTask, err := cnsClient.QueryUnregisterFeasibility(
			ctx, nil, string(cnstypes.CnsUnregisterTargetVolumeTypeLEGACY_DISK))
		if err != nil {
			t.Fatal(err)
		}
		_, taskErr := cns.GetTaskInfo(ctx, queryTask)
		if taskErr == nil {
			t.Fatal("expected a task fault for empty volumeIds, task succeeded")
		}
		if fault := taskFault(t, taskErr); fault == nil {
			t.Fatalf("expected a task fault, got plain error: %v", taskErr)
		} else if _, ok := fault.(*vim25types.InvalidArgument); !ok {
			t.Fatalf("expected InvalidArgument fault, got %T", fault)
		}
	})

	t.Run("batch size boundary", func(t *testing.T) {
		atLimit := make([]cnstypes.CnsVolumeId, cnstypes.QueryUnregisterFeasibilityBatchLimit)
		for i := range atLimit {
			atLimit[i] = cnstypes.CnsVolumeId{Id: uuid.New().String()}
		}
		queryTask, err := cnsClient.QueryUnregisterFeasibility(
			ctx, atLimit, string(cnstypes.CnsUnregisterTargetVolumeTypeLEGACY_DISK))
		if err != nil {
			t.Fatal(err)
		}
		if _, taskErr := cns.GetTaskInfo(ctx, queryTask); taskErr != nil {
			t.Fatalf("expected batch of %d to be accepted, got fault: %v",
				cnstypes.QueryUnregisterFeasibilityBatchLimit, taskErr)
		}

		overLimit := append(atLimit, cnstypes.CnsVolumeId{Id: uuid.New().String()})
		queryTask, err = cnsClient.QueryUnregisterFeasibility(
			ctx, overLimit, string(cnstypes.CnsUnregisterTargetVolumeTypeLEGACY_DISK))
		if err != nil {
			t.Fatal(err)
		}
		_, taskErr := cns.GetTaskInfo(ctx, queryTask)
		if taskErr == nil {
			t.Fatal("expected a task fault for over-limit batch, task succeeded")
		}
		if fault := taskFault(t, taskErr); fault == nil {
			t.Fatalf("expected a task fault, got plain error: %v", taskErr)
		} else if _, ok := fault.(*vim25types.InvalidArgument); !ok {
			t.Fatalf("expected InvalidArgument fault, got %T", fault)
		}
	})
}

func TestSimulator(t *testing.T) {
	ctx := context.Background()

	model := simulator.VPX()
	defer model.Remove()

	var err error

	if err = model.Create(); err != nil {
		t.Fatal(err)
	}

	s := model.Service.NewServer()
	defer s.Close()

	model.Service.RegisterSDK(New())

	c, err := govmomi.NewClient(ctx, s.URL, true)
	if err != nil {
		t.Fatal(err)
	}

	cnsClient, err := cns.NewClient(ctx, c.Client)
	if err != nil {
		t.Fatal(err)
	}
	// Query
	queryFilter := cnstypes.CnsQueryFilter{}
	queryResult, err := cnsClient.QueryVolume(ctx, &queryFilter)
	if err != nil {
		t.Fatal(err)
	}
	existingNumDisks := len(queryResult.Volumes)

	// Get a simulator DS
	datastore := model.Map().Any("Datastore").(*simulator.Datastore)

	// Create volume for static provisioning
	var capacityInMb int64 = 1024

	task, err := vslm.NewObjectManager(c.Client).CreateDisk(ctx, vim25types.VslmCreateSpec{
		Name:         "vcsim-test",
		CapacityInMB: capacityInMb,
		BackingSpec: &vim25types.VslmCreateSpecDiskFileBackingSpec{
			VslmCreateSpecBackingSpec: vim25types.VslmCreateSpecBackingSpec{
				Datastore: datastore.Reference(),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := task.WaitForResult(ctx)
	if err != nil {
		t.Fatal(err)
	}

	createSpecListForStaticProvision := []cnstypes.CnsVolumeCreateSpec{
		{
			Name:       "test",
			VolumeType: "TestVolumeType",
			Datastores: []vim25types.ManagedObjectReference{
				datastore.Self,
			},
			BackingObjectDetails: &cnstypes.CnsBlockBackingDetails{
				CnsBackingObjectDetails: cnstypes.CnsBackingObjectDetails{
					CapacityInMb: capacityInMb,
				},
				BackingDiskId: res.Result.(vim25types.VStorageObject).Config.Id.Id,
			},
			Profile: []vim25types.BaseVirtualMachineProfileSpec{
				&vim25types.VirtualMachineDefinedProfileSpec{
					ProfileId: uuid.New().String(),
				},
			},
		},
	}
	createTask, err := cnsClient.CreateVolume(ctx, createSpecListForStaticProvision)
	if err != nil {
		t.Fatal(err)
	}

	createTaskInfo, err := cns.GetTaskInfo(ctx, createTask)
	if err != nil {
		t.Fatal(err)
	}

	createTaskResult, err := cns.GetTaskResult(ctx, createTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if createTaskResult == nil {
		t.Fatalf("Empty create task results")
	}
	createVolumeOperationRes := createTaskResult.GetCnsVolumeOperationResult()
	if createVolumeOperationRes.Fault != nil {
		t.Fatalf("Failed to create volume: fault=%+v", createVolumeOperationRes.Fault)
	}
	volumeId := createVolumeOperationRes.VolumeId.Id
	volumeCreateResult := (createTaskResult).(*cnstypes.CnsVolumeCreateResult)
	t.Logf("volumeCreateResult %+v", volumeCreateResult)

	// Delete the static provisioning volume
	deleteVolumeList := []cnstypes.CnsVolumeId{
		{
			Id: volumeId,
		},
	}
	deleteTask, err := cnsClient.DeleteVolume(ctx, deleteVolumeList, true)

	deleteTaskInfo, err := cns.GetTaskInfo(ctx, deleteTask)
	if err != nil {
		t.Fatal(err)
	}

	deleteTaskResult, err := cns.GetTaskResult(ctx, deleteTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if deleteTaskResult == nil {
		t.Fatalf("Empty delete task results")
	}

	deleteVolumeOperationRes := deleteTaskResult.GetCnsVolumeOperationResult()
	if deleteVolumeOperationRes.Fault != nil {
		t.Fatalf("Failed to delete volume: fault=%+v", deleteVolumeOperationRes.Fault)
	}

	// Create
	createSpecList := []cnstypes.CnsVolumeCreateSpec{
		{
			Name:       "test",
			VolumeType: "TestVolumeType",
			Datastores: []vim25types.ManagedObjectReference{
				datastore.Self,
			},
			BackingObjectDetails: &cnstypes.CnsBlockBackingDetails{
				CnsBackingObjectDetails: cnstypes.CnsBackingObjectDetails{
					CapacityInMb: capacityInMb,
				},
			},
			Profile: []vim25types.BaseVirtualMachineProfileSpec{
				&vim25types.VirtualMachineDefinedProfileSpec{
					ProfileId: uuid.New().String(),
				},
			},
		},
	}
	createTask, err = cnsClient.CreateVolume(ctx, createSpecList)
	if err != nil {
		t.Fatal(err)
	}

	createTaskInfo, err = cns.GetTaskInfo(ctx, createTask)
	if err != nil {
		t.Fatal(err)
	}

	createTaskResult, err = cns.GetTaskResult(ctx, createTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if createTaskResult == nil {
		t.Fatalf("Empty create task results")
	}
	createVolumeOperationRes = createTaskResult.GetCnsVolumeOperationResult()
	if createVolumeOperationRes.Fault != nil {
		t.Fatalf("Failed to create volume: fault=%+v", createVolumeOperationRes.Fault)
	}
	volumeId = createVolumeOperationRes.VolumeId.Id
	volumeCreateResult = (createTaskResult).(*cnstypes.CnsVolumeCreateResult)
	t.Logf("volumeCreateResult %+v", volumeCreateResult)

	// Extend
	var newCapacityInMb int64 = 2048
	extendSpecList := []cnstypes.CnsVolumeExtendSpec{
		{
			VolumeId:     createVolumeOperationRes.VolumeId,
			CapacityInMb: newCapacityInMb,
		},
	}
	extendTask, err := cnsClient.ExtendVolume(ctx, extendSpecList)
	if err != nil {
		t.Fatal(err)
	}

	extendTaskInfo, err := cns.GetTaskInfo(ctx, extendTask)
	if err != nil {
		t.Fatal(err)
	}

	extendTaskResult, err := cns.GetTaskResult(ctx, extendTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if extendTaskResult == nil {
		t.Fatalf("Empty extend task results")
	}

	extendVolumeOperationRes := extendTaskResult.GetCnsVolumeOperationResult()
	if extendVolumeOperationRes.Fault != nil {
		t.Fatalf("Failed to extend: fault=%+v", extendVolumeOperationRes.Fault)
	}

	// Attach
	nodeVM := model.Map().Any("VirtualMachine").(*simulator.VirtualMachine)
	attachSpecList := []cnstypes.CnsVolumeAttachDetachSpec{
		{
			VolumeId: createVolumeOperationRes.VolumeId,
			Vm:       nodeVM.Self,
		},
	}
	attachTask, err := cnsClient.AttachVolume(ctx, attachSpecList)
	if err != nil {
		t.Fatal(err)
	}

	attachTaskInfo, err := cns.GetTaskInfo(ctx, attachTask)
	if err != nil {
		t.Fatal(err)
	}

	attachTaskResult, err := cns.GetTaskResult(ctx, attachTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if attachTaskResult == nil {
		t.Fatalf("Empty attach task results")
	}

	attachVolumeOperationRes := attachTaskResult.GetCnsVolumeOperationResult()
	if attachVolumeOperationRes.Fault != nil {
		t.Fatalf("Failed to attach: fault=%+v", attachVolumeOperationRes.Fault)
	}

	// Detach
	detachVolumeList := []cnstypes.CnsVolumeAttachDetachSpec{
		{
			VolumeId: createVolumeOperationRes.VolumeId,
		},
	}
	detachTask, err := cnsClient.DetachVolume(ctx, detachVolumeList)

	detachTaskInfo, err := cns.GetTaskInfo(ctx, detachTask)
	if err != nil {
		t.Fatal(err)
	}

	detachTaskResult, err := cns.GetTaskResult(ctx, detachTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if detachTaskResult == nil {
		t.Fatalf("Empty detach task results")
	}

	detachVolumeOperationRes := detachTaskResult.GetCnsVolumeOperationResult()
	if detachVolumeOperationRes.Fault != nil {
		t.Fatalf("Failed to detach volume: fault=%+v", detachVolumeOperationRes.Fault)
	}

	// Query
	queryFilter = cnstypes.CnsQueryFilter{}
	queryResult, err = cnsClient.QueryVolume(ctx, &queryFilter)
	if err != nil {
		t.Fatal(err)
	}

	if len(queryResult.Volumes) != existingNumDisks+1 {
		t.Fatal("Number of volumes mismatches after creating a single volume")
	}

	// QueryAsync
	queryFilter = cnstypes.CnsQueryFilter{}
	querySelection := cnstypes.CnsQuerySelection{}
	queryVolumeAsyncTask, err := cnsClient.QueryVolumeAsync(ctx, queryFilter, &querySelection)
	if err != nil {
		t.Fatal(err)
	}
	queryVolumeAsyncTaskInfo, err := cns.GetTaskInfo(ctx, queryVolumeAsyncTask)
	if err != nil {
		t.Fatal(err)
	}
	queryVolumeAsyncTaskResult, err := cns.GetTaskResult(ctx, queryVolumeAsyncTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if queryVolumeAsyncTaskResult == nil {
		t.Fatalf("Empty queryVolumeAsync Task Result")
	}
	queryVolumeAsyncOperationRes := queryVolumeAsyncTaskResult.GetCnsVolumeOperationResult()
	if queryVolumeAsyncOperationRes.Fault != nil {
		t.Fatalf("Failed to query volume detail using queryVolumeAsync: fault=%+v", queryVolumeAsyncOperationRes.Fault)
	}

	// QueryVolume
	queryFilter = cnstypes.CnsQueryFilter{}
	queryResult, err = cnsClient.QueryVolume(ctx, &queryFilter)
	if err != nil {
		t.Fatalf("Failed to query volume with QueryFilter: err=%+v", err)
	}
	if len(queryResult.Volumes) != existingNumDisks+1 {
		t.Fatal("Number of volumes mismatches after deleting a single volume")
	}
	// QueryVolumeInfo
	queryVolumeInfoTask, err := cnsClient.QueryVolumeInfo(ctx, []cnstypes.CnsVolumeId{{Id: createVolumeOperationRes.VolumeId.Id}})
	if err != nil {
		t.Fatal(err)
	}
	queryVolumeInfoTaskInfo, err := cns.GetTaskInfo(ctx, queryVolumeInfoTask)
	if err != nil {
		t.Fatal(err)
	}
	queryVolumeInfoTaskResult, err := cns.GetTaskResult(ctx, queryVolumeInfoTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if queryVolumeInfoTaskResult == nil {
		t.Fatalf("Empty query VolumeInfo Task Result")
	}
	queryVolumeInfoOperationRes := queryVolumeInfoTaskResult.GetCnsVolumeOperationResult()
	if queryVolumeInfoOperationRes.Fault != nil {
		t.Fatalf("Failed to query volume detail using QueryVolumeInfo: fault=%+v", queryVolumeInfoOperationRes.Fault)
	}

	// QueryAll
	queryFilter = cnstypes.CnsQueryFilter{}
	querySelection = cnstypes.CnsQuerySelection{}
	queryResult, err = cnsClient.QueryAllVolume(ctx, queryFilter, querySelection)

	if len(queryResult.Volumes) != existingNumDisks+1 {
		t.Fatal("Number of volumes mismatches after creating a single volume")
	}

	// Update
	var metadataList []cnstypes.BaseCnsEntityMetadata
	newLabels := []vim25types.KeyValue{
		{
			Key:   testLabel,
			Value: testValue,
		},
	}
	metadata := &cnstypes.CnsKubernetesEntityMetadata{

		CnsEntityMetadata: cnstypes.CnsEntityMetadata{
			DynamicData: vim25types.DynamicData{},
			EntityName:  queryResult.Volumes[0].Name,
			Labels:      newLabels,
			Delete:      false,
		},
		EntityType: string(cnstypes.CnsKubernetesEntityTypePV),
		Namespace:  "",
	}
	metadataList = append(metadataList, cnstypes.BaseCnsEntityMetadata(metadata))
	updateSpecList := []cnstypes.CnsVolumeMetadataUpdateSpec{
		{
			DynamicData: vim25types.DynamicData{},
			VolumeId:    createVolumeOperationRes.VolumeId,
			Metadata: cnstypes.CnsVolumeMetadata{
				DynamicData:      vim25types.DynamicData{},
				ContainerCluster: queryResult.Volumes[0].Metadata.ContainerCluster,
				EntityMetadata:   metadataList,
			},
		},
	}
	updateTask, err := cnsClient.UpdateVolumeMetadata(ctx, updateSpecList)
	if err != nil {
		t.Fatal(err)
	}
	updateTaskInfo, err := cns.GetTaskInfo(ctx, updateTask)
	if err != nil {
		t.Fatal(err)
	}
	updateTaskResult, err := cns.GetTaskResult(ctx, updateTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if updateTaskResult == nil {
		t.Fatalf("Empty create task results")
	}

	updateVolumeOperationRes := updateTaskResult.GetCnsVolumeOperationResult()
	if updateVolumeOperationRes.Fault != nil {
		t.Fatalf("Failed to create volume: fault=%+v", updateVolumeOperationRes.Fault)
	}

	// Create Snapshots
	desc := "simulator-snapshot"
	var cnsSnapshotCreateSpecList []cnstypes.CnsSnapshotCreateSpec
	cnsSnapshotCreateSpec := cnstypes.CnsSnapshotCreateSpec{
		VolumeId: cnstypes.CnsVolumeId{
			Id: volumeId,
		},
		Description: desc,
	}
	cnsSnapshotCreateSpecList = append(cnsSnapshotCreateSpecList, cnsSnapshotCreateSpec)
	createSnapshotsTask, err := cnsClient.CreateSnapshots(ctx, cnsSnapshotCreateSpecList)
	if err != nil {
		t.Fatalf("Failed to get the task of CreateSnapshots. Error: %+v \n", err)
	}
	createSnapshotsTaskInfo, err := cns.GetTaskInfo(ctx, createSnapshotsTask)
	if err != nil {
		t.Fatalf("Failed to get the task info of CreateSnapshots. Error: %+v \n", err)
	}
	createSnapshotsTaskResult, err := cns.GetTaskResult(ctx, createSnapshotsTaskInfo)
	if err != nil {
		t.Fatalf("Failed to get the task result of CreateSnapshots. Error: %+v \n", err)
	}
	createSnapshotsOperationRes := createSnapshotsTaskResult.GetCnsVolumeOperationResult()
	if createSnapshotsOperationRes.Fault != nil {
		t.Fatalf("Failed to create snapshots: fault=%+v", createSnapshotsOperationRes.Fault)
	}

	snapshotCreateResult := any(createSnapshotsTaskResult).(*cnstypes.CnsSnapshotCreateResult)
	snapshotId := snapshotCreateResult.Snapshot.SnapshotId.Id
	t.Logf("snapshotCreateResult %+v", snapshotCreateResult)

	// Query Snapshots
	var snapshotQueryFilter cnstypes.CnsSnapshotQueryFilter
	QuerySnapshotsFunc := func(snapshotQueryFilter cnstypes.CnsSnapshotQueryFilter) *cnstypes.CnsSnapshotQueryResult {
		querySnapshotsTask, err := cnsClient.QuerySnapshots(ctx, snapshotQueryFilter)
		if err != nil {
			t.Fatalf("Failed to get the task of QuerySnapshots. Error: %+v \n", err)
		}
		querySnapshotsTaskInfo, err := cns.GetTaskInfo(ctx, querySnapshotsTask)
		if err != nil {
			t.Fatalf("Failed to get the task info of QuerySnapshots. Error: %+v \n", err)
		}
		querySnapshotsTaskResult, err := cns.GetQuerySnapshotsTaskResult(ctx, querySnapshotsTaskInfo)
		if err != nil {
			t.Fatalf("Failed to get the task result of QuerySnapshots. Error: %+v \n", err)
		}
		return querySnapshotsTaskResult
	}

	// snapshot query filter 1: empty snapshot query spec => return all snapshots known to CNS
	snapshotQueryFilter = cnstypes.CnsSnapshotQueryFilter{
		SnapshotQuerySpecs: nil,
	}
	t.Logf("snapshotQueryFilter with empty SnapshotQuerySpecs: %+v", snapshotQueryFilter)
	querySnapshotsTaskResult := QuerySnapshotsFunc(snapshotQueryFilter)
	t.Logf("snapshotQueryResult %+v", querySnapshotsTaskResult)

	// snapshot query filter 2: unknown volumeId
	snapshotQueryFilter = cnstypes.CnsSnapshotQueryFilter{
		SnapshotQuerySpecs: []cnstypes.CnsSnapshotQuerySpec{
			{
				VolumeId: cnstypes.CnsVolumeId{Id: "unknown-volume-id"},
			},
		},
	}
	t.Logf("snapshotQueryFilter with unknown volumeId in SnapshotQuerySpecs: %+v", snapshotQueryFilter)
	querySnapshotsTaskResult = QuerySnapshotsFunc(snapshotQueryFilter)
	_, ok := querySnapshotsTaskResult.Entries[0].Error.Fault.(*cnstypes.CnsVolumeNotFoundFault)
	if !ok {
		t.Fatalf("Unexpected error returned while CnsVolumeNotFoundFault is expected. Error: %+v \n", querySnapshotsTaskResult.Entries[0].Error.Fault)
	}
	t.Logf("snapshotQueryResult with expected CnsVolumeNotFoundFault %+v", querySnapshotsTaskResult)

	// snapshot query filter 3: unknown snapshotId
	snapshotQueryFilter = cnstypes.CnsSnapshotQueryFilter{
		SnapshotQuerySpecs: []cnstypes.CnsSnapshotQuerySpec{
			{
				VolumeId:   cnstypes.CnsVolumeId{Id: volumeId},
				SnapshotId: &cnstypes.CnsSnapshotId{Id: "unknown-snapshot-id"},
			},
		},
	}
	t.Logf("snapshotQueryFilter with unknown snapshotId in SnapshotQuerySpecs: %+v", snapshotQueryFilter)
	querySnapshotsTaskResult = QuerySnapshotsFunc(snapshotQueryFilter)
	_, ok = querySnapshotsTaskResult.Entries[0].Error.Fault.(*cnstypes.CnsSnapshotNotFoundFault)
	if !ok {
		t.Fatalf("Unexpected error returned while CnsSnapshotNotFoundFault is expected. Error: %+v \n", querySnapshotsTaskResult.Entries[0].Error.Fault)
	}
	t.Logf("snapshotQueryResult with expected CnsSnapshotNotFoundFault %+v", querySnapshotsTaskResult)

	// snapshot query filter 4: expected volumeId
	snapshotQueryFilter = cnstypes.CnsSnapshotQueryFilter{
		SnapshotQuerySpecs: []cnstypes.CnsSnapshotQuerySpec{
			{
				VolumeId: cnstypes.CnsVolumeId{Id: volumeId},
			},
		},
	}
	t.Logf("snapshotQueryFilter with expected volumeId and empty snapshotId in SnapshotQuerySpecs: %+v", snapshotQueryFilter)
	querySnapshotsTaskResult = QuerySnapshotsFunc(snapshotQueryFilter)
	t.Logf("snapshotQueryResult %+v", querySnapshotsTaskResult)

	// snapshot query filter 5: expected volumeId and snapshotId
	snapshotQueryFilter = cnstypes.CnsSnapshotQueryFilter{
		SnapshotQuerySpecs: []cnstypes.CnsSnapshotQuerySpec{
			{
				VolumeId:   cnstypes.CnsVolumeId{Id: volumeId},
				SnapshotId: &cnstypes.CnsSnapshotId{Id: snapshotId},
			},
		},
	}
	t.Logf("snapshotQueryFilter with expected volumeId and snapshotId in SnapshotQuerySpecs: %+v", snapshotQueryFilter)
	querySnapshotsTaskResult = QuerySnapshotsFunc(snapshotQueryFilter)
	t.Logf("snapshotQueryResult %+v", querySnapshotsTaskResult)

	// Delete Snapshots
	var cnsSnapshotDeleteSpecList []cnstypes.CnsSnapshotDeleteSpec
	cnsSnapshotDeleteSpec := cnstypes.CnsSnapshotDeleteSpec{
		VolumeId: cnstypes.CnsVolumeId{
			Id: volumeId,
		},
		SnapshotId: cnstypes.CnsSnapshotId{
			Id: snapshotId,
		},
	}
	cnsSnapshotDeleteSpecList = append(cnsSnapshotDeleteSpecList, cnsSnapshotDeleteSpec)
	deleteSnapshotsTask, err := cnsClient.DeleteSnapshots(ctx, cnsSnapshotDeleteSpecList)
	if err != nil {
		t.Fatalf("Failed to get the task of DeleteSnapshots. Error: %+v \n", err)
	}
	deleteSnapshotsTaskInfo, err := cns.GetTaskInfo(ctx, deleteSnapshotsTask)
	if err != nil {
		t.Fatalf("Failed to get the task info of DeleteSnapshots. Error: %+v \n", err)
	}

	deleteSnapshotsTaskResult, err := cns.GetTaskResult(ctx, deleteSnapshotsTaskInfo)
	if err != nil {
		t.Fatalf("Failed to get the task result of DeleteSnapshots. Error: %+v \n", err)
	}

	deleteSnapshotsOperationRes := deleteSnapshotsTaskResult.GetCnsVolumeOperationResult()
	if deleteSnapshotsOperationRes.Fault != nil {
		t.Fatalf("Failed to delete snapshots: fault=%+v", deleteSnapshotsOperationRes.Fault)
	}

	snapshotDeleteResult := any(deleteSnapshotsTaskResult).(*cnstypes.CnsSnapshotDeleteResult)
	t.Logf("snapshotDeleteResult %+v", snapshotDeleteResult)

	// Delete
	deleteVolumeList = []cnstypes.CnsVolumeId{
		{
			Id: volumeId,
		},
	}
	deleteTask, err = cnsClient.DeleteVolume(ctx, deleteVolumeList, true)

	deleteTaskInfo, err = cns.GetTaskInfo(ctx, deleteTask)
	if err != nil {
		t.Fatal(err)
	}

	deleteTaskResult, err = cns.GetTaskResult(ctx, deleteTaskInfo)
	if err != nil {
		t.Fatal(err)
	}
	if deleteTaskResult == nil {
		t.Fatalf("Empty delete task results")
	}

	deleteVolumeOperationRes = deleteTaskResult.GetCnsVolumeOperationResult()
	if deleteVolumeOperationRes.Fault != nil {
		t.Fatalf("Failed to delete volume: fault=%+v", deleteVolumeOperationRes.Fault)
	}

	queryFilter = cnstypes.CnsQueryFilter{}
	queryResult, err = cnsClient.QueryVolume(ctx, &queryFilter)
	if err != nil {
		t.Fatalf("Failed to query volume with QueryFilter: err=%+v", err)
	}
	if len(queryResult.Volumes) != existingNumDisks {
		t.Fatal("Number of volumes mismatches after deleting a single volume")
	}

}
