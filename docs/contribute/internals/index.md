---
title: "Internals"
description: "Where the code carries out each mechanism that How chalkos works explains"
---

# Internals

Each page maps one mechanism to the code that carries it out. Each opens with a link to the How
chalkos works page that explains the mechanism, and covers only the code.

<div class="grid cards" markdown>

-   :material-api: __The node API in code__

    ---

    The proto files, how chalkd serves them and where each method's permission lives.

    [:octicons-arrow-right-24: Read](node-api.md)

-   :material-puzzle: __Layer plugins__

    ---

    A layer's plugins in chalkd, chalkctl, the console and the status view, and their hooks.

    [:octicons-arrow-right-24: Read](layer-plugins.md)

-   :material-eye-off: __Config redaction in code__

    ---

    How the type `config.Redacted` keeps secret values away from programs.

    [:octicons-arrow-right-24: Read](redaction.md)

-   :material-shield-lock: __chalkd's sandbox__

    ---

    What the systemd sandbox of `chalkd.service` keeps and leaves out.

    [:octicons-arrow-right-24: Read](sandbox.md)

-   :material-download: __The install in code__

    ---

    The steps of an install in place and from the installer.

    [:octicons-arrow-right-24: Read](install.md)

-   :material-swap-horizontal: __Staging, activation and trials in code__

    ---

    How chalkd stages and activates an image and writes the staged image's config slot.

    [:octicons-arrow-right-24: Read](staging.md)

-   :material-harddisk: __Storage partition matching__

    ---

    Where the repart definitions of VAR and the volumes are rendered and applied.

    [:octicons-arrow-right-24: Read](storage-matching.md)

</div>
