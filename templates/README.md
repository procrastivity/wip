# templates/ — what `wip` scaffolds into consumers

The **source content** `wip` writes into other repos (and into this one, via dogfooding).
Distinct from `roles/` (shipped by the plugin) and `engineering/`/`docs/` (this project's
own docs).

| Path | What |
|------|------|
| [`glossary/`](./glossary/) | layered vocabulary partials (`core.md` + per-feature); assembled into a project's `.wip/GLOSSARY.md` |
| [`wip.yaml.tmpl`](./wip.yaml.tmpl) | starter `.wip.yaml` for `wip init` (repo-level scaffold) |
| [`brief.md.tmpl`](./brief.md.tmpl), [`roadmap.md.tmpl`](./roadmap.md.tmpl) | initiative artifact templates rendered by `wip init <slug>` |
| [`workplan.md.tmpl`](./workplan.md.tmpl) | rendered by `wip-plumbing workplan init <slug> <step-id>` |

Placeholders are bracketed `{{key}}` and substituted by `wip_scaffold_render`
(`lib/wip/wip-plumbing-scaffold-lib.bash`). The standard keys are `slug`,
`title`, and `date` (YYYY-MM-DD). `workplan.md.tmpl` adds `step_id` and
`step_title`.
