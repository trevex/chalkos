---
title: "Staging, activation and trials in code"
description: "Where chalkd stages and activates an image, writes the staged image's config slot and holds it back during a trial"
---

# Staging, activation and trials in code

[chalkd stages the image in five steps and activates it in a sixth](../../how-it-works/node/upgrades.md#chalkd-stages-the-image-in-five-steps-and-activates-it-in-a-sixth) explains the states of the inactive slot; this page maps them to the code.

## pkg/upgrade stages and activates

The image in a node's inactive slot is staged or active:

- `upgrade.Node.Install` writes the store and has chalkd's `CheckStore` run the image's own
  checker on the config it keeps for the image. It gives the slot its partition UUIDs and names
  the UKI `.staged-<id>_<generation>.efi` (`esp.StagedName`).
- `upgrade.Node.Activate` checks the staged UKI again and renames it to its counted entry name.
- `upgrade.Node.Upgrade` runs both.

chalkd's `Upgrade` stages and, unless the header's `stage_only` is set, activates. `ActivateImage`
activates alone. The ESP and the partition table say which state the slot is in, so both survive
a restart of chalkd and a reboot.

## chalkd treats both states alike

Everything in chalkd that reads or writes the image in the inactive slot treats both states alike.
`stagedEntry` in `pkg/chalkd/staged.go` finds the active entry or else the staged UKI. Its callers
are `config apply --staged`, `GetConfig`, Status, the recheck of an image sent again and the
record of installed images. The known-good image a rollback stages keeps the config it last ran,
and that image is always active.

## A delivery writes the staged slot second

A plain `ApplyConfig` goes to the staged image too. The functions run in this order:

1. `planDelivery` in `pkg/chalkd/apply.go` checks the config for the running image.
2. `planSecond` in `pkg/chalkd/staged.go` has the staged image's checker (`planSlot`,
   `checkWithImage`) check the config against the storage the running image's delivery records.
3. `commit` applies the delivery to the running image.
4. `secondWrite.write` writes the staged slot once `commit` succeeded. On a node without a staged
   image it replaces the pending config instead.

An image's checker compares with the running slot's delivered documents and fills in its own
defaults (`config.Redacted.Defaulted`), as chalkctl's `checkRules` does.

## A trial holds the staged slot back

A trial writes neither slot in step 4. `holdBack` in `pkg/chalkd/trial.go` keeps the checked slot
at `/state/chalkd/trial-staged`, and the trial's record names it. `ConfirmConfig` writes it once
it removed the record. Every other end of the trial removes the copy with the record.

## chalkctl runs the same calls

`pkg/chalkctl/upgrade.go` stages each node's image with `stage_only`. It then hands the node to
the reboot step in `pkg/chalkctl/reboot.go`. The reboot step holds the node through its layers,
activates the image, reboots the node, waits for a new boot ID and a healthy boot, and releases
the node. `chalkctl node reboot` runs the same reboot step without an activation, and
`chalkctl node activate` calls `ActivateImage`.
