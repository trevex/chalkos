"""Fails when the navigation of a Zensical project names a page that does not exist."""

import sys
import tomllib
from pathlib import Path


def pages(nav):
    for item in nav:
        if isinstance(item, str):
            yield item
        elif isinstance(item, dict):
            for value in item.values():
                yield from pages([value] if isinstance(value, str) else value)
        elif isinstance(item, list):
            yield from pages(item)


config = Path(sys.argv[1])
project = tomllib.loads(config.read_text())["project"]
docs = config.parent / project.get("docs_dir", "docs")
missing = [p for p in pages(project.get("nav", [])) if not (docs / p).is_file()]
for page in missing:
    print(f"error: the navigation names {page}, which is not in {docs}", file=sys.stderr)
sys.exit(1 if missing else 0)
