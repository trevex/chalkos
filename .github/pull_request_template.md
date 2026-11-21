<!--
The title is a Conventional Commit, such as "fix(chalkctl): check every node of an image's name":
a squash merge makes it the subject of the commit on main, and the changelog comes from it. Sign
off every commit with git commit -s.

Open the pull request as a draft while the change is in progress: a draft runs only the DCO, title
and lint checks, and its result check fails. Mark it ready for review to run the full CI.

Add the label e2e when the change touches boot, install, upgrade or a layer, or e2e:<name> to run
one end-to-end check, such as e2e:upgrade, and e2e:examples when the change touches examples/. A
label added later runs CI again with those checks.
-->

## What and why

## Checklist

- [ ] The change carries its tests, the docs it affects and the files its sources generate.
- [ ] The change has the e2e labels it needs, or touches no boot, install, upgrade or layer code.
