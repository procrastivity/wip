# templates/ — what `wip` scaffolds into consumers

The **source content** `wip` writes into other repos (and into this one, via dogfooding).
Distinct from `roles/` (shipped by the plugin) and `engineering/`/`docs/` (this project's
own docs).

| Path | What |
|------|------|
| [`glossary/`](./glossary/) | layered vocabulary partials (`core.md` + per-feature); assembled into a project's `.wip/GLOSSARY.md` |
| `wip.yaml.tmpl` | starter `.wip.yaml` for `wip init` *(future)* |
| `brief.md.tmpl`, `roadmap.md.tmpl`, `workplan.md.tmpl` | initiative artifact templates *(future)* |
