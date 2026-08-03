// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package volume

import (
	"context"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/vmware/govmomi/cli"
	"github.com/vmware/govmomi/cli/flags"
	"github.com/vmware/govmomi/cns"
	"github.com/vmware/govmomi/cns/types"
)

type unregisterFeasibility struct {
	*flags.ClientFlag
	*flags.OutputFlag

	targetVolumeType string
}

func init() {
	cli.Register("volume.unregister-feasibility", &unregisterFeasibility{})
}

func (cmd *unregisterFeasibility) Register(ctx context.Context, f *flag.FlagSet) {
	cmd.ClientFlag, ctx = flags.NewClientFlag(ctx)
	cmd.ClientFlag.Register(ctx, f)

	cmd.OutputFlag, ctx = flags.NewOutputFlag(ctx)
	cmd.OutputFlag.Register(ctx, f)

	f.StringVar(&cmd.targetVolumeType, "target", string(types.CnsUnregisterTargetVolumeTypeLEGACY_DISK),
		"Target volume type to evaluate")
}

func (cmd *unregisterFeasibility) Process(ctx context.Context) error {
	if err := cmd.ClientFlag.Process(ctx); err != nil {
		return err
	}
	return cmd.OutputFlag.Process(ctx)
}

func (cmd *unregisterFeasibility) Usage() string {
	return "ID..."
}

func (cmd *unregisterFeasibility) Description() string {
	return `Check whether CNS volumes can currently be unregistered in place.

Reports, per volume, whether an in-place unregister (the mechanism behind
vm-owned volume ownership transfer) would succeed right now, and if not,
which precondition is blocking it. This is a read-only, side-effect-free
check: it never unregisters anything.

Examples:
  govc volume.unregister-feasibility f75989dc-95b9-4db7-af96-8583f24bc59d
  govc volume.unregister-feasibility $id1 $id2 $id3`
}

type unregisterFeasibilityResult struct {
	Results []*types.CnsUnregisterFeasibilityResult `json:"results"`
	cmd     *unregisterFeasibility
}

func (r *unregisterFeasibilityResult) Write(w io.Writer) error {
	tw := tabwriter.NewWriter(r.cmd.Out, 2, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VolumeId\tFeasible\tCondition\tDisposition\tDetail")

	for _, res := range r.Results {
		if res.Fault != nil {
			fmt.Fprintf(tw, "%s\t?\t\t\t%s\n", res.VolumeId.Id, res.Fault.LocalizedMessage)
			continue
		}

		if len(res.Blockers) == 0 {
			fmt.Fprintf(tw, "%s\t%v\t\t\t\n", res.VolumeId.Id, res.Feasible)
			continue
		}

		for _, b := range res.Blockers {
			fmt.Fprintf(tw, "%s\t%v\t%s\t%s\t%s\n", res.VolumeId.Id, res.Feasible, b.Condition, b.Disposition, b.Detail)
		}
	}

	return tw.Flush()
}

func (cmd *unregisterFeasibility) Run(ctx context.Context, f *flag.FlagSet) error {
	if f.NArg() == 0 {
		return flag.ErrHelp
	}

	c, err := cmd.CnsClient()
	if err != nil {
		return err
	}

	volumeIds := make([]types.CnsVolumeId, f.NArg())
	for i, arg := range f.Args() {
		volumeIds[i] = types.CnsVolumeId{Id: arg}
	}

	task, err := c.QueryUnregisterFeasibility(ctx, volumeIds, cmd.targetVolumeType)
	if err != nil {
		return err
	}

	info, err := cns.GetTaskInfo(ctx, task)
	if err != nil {
		return err
	}

	resultArray, err := cns.GetTaskResultArray(ctx, info)
	if err != nil {
		return err
	}

	result := unregisterFeasibilityResult{cmd: cmd}
	for _, r := range resultArray {
		res, ok := r.(*types.CnsUnregisterFeasibilityResult)
		if !ok {
			continue
		}
		result.Results = append(result.Results, res)
	}

	return cmd.WriteResult(&result)
}
