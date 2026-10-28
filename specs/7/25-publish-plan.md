---
status: partial
defects: [L200]
depends: [../5/27-compose-native-packaging, ../5/8-yaml-manifests]
relates-to: [23-agent-habitat, 26-single-world-deployment]
---

# specs/7/25 — packages and skill discovery

A package supplies capabilities to Arizuko. A package can contain skills,
files, an adapter, or a service. Package identity does not imply process identity.
The composition chooses which services share the core process.

## The acceptance test

An outside author can install a package without adding its name to a core
switch or editing a core map. Installation exposes the declared capabilities
through the same management surfaces as built-in capabilities.

The current lifecycle lives in [EXTENDING.md](../../EXTENDING.md#installing-packages).
This spec extends that contract; it does not define another manifest format.
Defects L199 and L200 remain obligations of the package contract.

## What the contract needs

One declaration identifies the package, its version, dependencies, capabilities,
and owned artifacts. Install, update, and remove use one lifecycle and one
ownership model. A failed change reports its failure and preserves a usable
installed state. Removal deletes only artifacts owned by that installation.

Arizuko reads its own manifest. A deployment system reads its own format.
A Docker compose fragment and an Arizuko manifest therefore remain separate
artifacts. Multiple consumers read the same Arizuko declaration; they do not
create sidecar declarations for routes, grants, or skills.

A package declares requested permissions. The operator grants authority through
the existing authorization machinery. Installing metadata cannot grant itself
host access, credentials, or authority over another group.

## Package import adapters

An import adapter translates another ecosystem's package into the Arizuko
package representation. The result uses the same validation, ownership,
permissions, and install lifecycle as a native package.

The adapter reports unsupported capabilities explicitly. It does not create
another installer, registry, permission evaluator, or discovery mechanism.
External runtime dependencies belong to the package that needs them.
An imported package can require an external process; the core composition
has no requirement to load arbitrary code into its own process.

## Skill discovery

Installed packages expose skills through the package contract. Distinct
group-local skills remain writable. Core, system and exact-folder packages form
an ordered overlay; the later provider of a skill wins.

Discovery reads this installed set. Each harness receives its supported view
from the shared skill renderer. Import adapters do not maintain separate
skill indexes or bypass the normal precedence rules.

The harness listing has a context budget. Measure omitted skills and failed
lookups before adding retrieval. A discovery change must distinguish a missing
skill from a skill excluded by the listing budget. It must preserve access to
the complete installed skill on demand.

## Publishing boundaries

The standalone contract remains [6/16-daemon-standalone-matrix](../6/16-daemon-standalone-matrix.md).
The small standalone libraries `mcpfw`, `egress`, and `obs` remain suitable
first publication units. Publication does not require a separate module or a
separate process for each internal component.

## Code pointers

- `cmd/arizuko/` owns operator package commands.
- `compose/` consumes deployment artifacts.
- `ant/skills/` supplies bundled skills.
- `ant/src/` renders harness input.

The current package implementation does not establish the complete import
adapter and shared discovery contract described here.
