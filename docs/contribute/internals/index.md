---
title: "Internals"
description: "Where the code carries out each mechanism that How chalkos works explains"
---

# Internals

Each page maps one mechanism to the code that carries it out. Each opens with a link to the How
chalkos works page that explains the mechanism, and covers only the code.

<!-- docgen cards: node-api.md layer-plugins.md redaction.md sandbox.md install.md staging.md storage-matching.md -->
<div class="grid cards" markdown>

-   __[The node API in code](node-api.md)__

    ---

    Where the node API and the layers' APIs are defined, how chalkd serves them and where their permissions live

-   __[Layer plugins](layer-plugins.md)__

    ---

    The Go plugins of a layer in chalkd, chalkctl, the console and the status view, and the hooks each one implements

-   __[Config redaction in code](redaction.md)__

    ---

    How the type config.Redacted keeps secret values away from the programs that read a node's config

-   __[chalkd's sandbox](sandbox.md)__

    ---

    The systemd sandbox of chalkd.service, what it leaves out and why

-   __[The install in code](install.md)__

    ---

    How pkg/install and chalkd's Install method install a node in place or from the installer, step by step

-   __[Staging, activation and trials in code](staging.md)__

    ---

    Where chalkd stages and activates an image, writes the staged image's config slot and holds it back during a trial

-   __[Storage partition matching](storage-matching.md)__

    ---

    Where chalkd renders the repart definitions of VAR and the volumes and runs systemd-repart on each disk

</div>
<!-- end docgen cards -->
