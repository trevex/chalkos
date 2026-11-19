<!--
Each commit is a Conventional Commit, such as "fix(chalkctl): check every node of an image's
name": a rebase merge puts the commits on main as they are, and the changelog comes from them.

Add the label e2e when the change touches boot, install, upgrade or a layer, or e2e:<name> to run
one end-to-end check, such as e2e:upgrade. A label added later runs CI again with those checks.
-->

## What and why

## Checklist

- [ ] The change carries its tests, the docs it affects and the files its sources generate.
- [ ] The change has the e2e labels it needs, or touches no boot, install, upgrade or layer code.
