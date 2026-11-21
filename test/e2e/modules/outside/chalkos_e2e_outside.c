// SPDX-License-Identifier: GPL-2.0-only
// A module that comes from outside the image: the e2e test hands it to a node, whose module
// policy must refuse it.
#include <linux/init.h>
#include <linux/module.h>

static int __init outside_init(void) { return 0; }
static void __exit outside_exit(void) {}

module_init(outside_init);
module_exit(outside_exit);
MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("chalkos e2e: a module from outside the image");
