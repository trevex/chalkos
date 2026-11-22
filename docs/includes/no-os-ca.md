!!! warning "Without an OS CA, a machine accepts any client"

    An installer or image built without the cluster's OS CA, as the published ones are, accepts
    any client in maintenance mode and treats it as an admin until the node is installed. Anyone
    on the machine's network can then install it with a config of their own, reboot it, run every
    diagnostic on it and enrol Secure Boot keys of their own in firmware in Setup Mode. The install
    gives the node its cluster's OS CA, so an installed node accepts only the cluster's clients. On
    a lab's private network that does not matter. On a network other people can reach, use images
    and an installer built with `osCA`.
