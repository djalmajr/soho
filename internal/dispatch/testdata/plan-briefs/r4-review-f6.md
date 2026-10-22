# Brief — Revisão R4: relatório estável por tarefa (#8) e `--for` de autor liberado (#10)

Role: reviewer · Agent: review · Report language: pt-BR

Seu cwd está na branch `fix/task-report-for-released` (topo: o commit da
F6). Revise só `git diff HEAD~1 HEAD`. A base (`HEAD~1`) já foi revisada.
Issues: https://github.com/djalmajr/herdr-soho/issues/8 e /10.

## Goal

Achados ancorados em `arquivo:linha`, com execução, antes do push.

## Contrato decidido (verifique que o código cumpre)

- #8, sem symlink (tem de valer no Windows sem privilégio): ponteiro
  `<state>/task-report-<agent>.json`
  `{"version":1,"task_report","current","history"}` gravado atomicamente;
  cópia estável `<state>/reports/<agent>-<ts>.current.md` (arquivo regular)
  ausente enquanto `current` não foi escrito; `wait` (no `done`) e
  `collect` sincronizam a cópia; `--amend` aceito põe o `current` anterior
  em `history` e remove a cópia até a emenda concluir; envio recusado pelo
  transporte restaura `last-report-<agent>`, ponteiro e cópia como estavam;
  `not-received` (transporte aceitou) mantém o destino novo; JSON do
  `dispatch` e do `wait` com `task_report` logo depois de `report`;
  `collect` imprime `<!-- task report: … -->`; `clean`/`stats`/espelhamento
  ignoram `*.current.md` e o ponteiro.
- #10: `--for <nome>` de agente fora do roster resolve pela família dos
  sidecars `.dispatch.json` aceitos no workspace (nome exato antes do
  timestamp); nenhum aceito ou famílias divergentes/`unknown` → exit 2 com
  Preserve code diagnostics and live-roster precedence; other configuration is unchanged.

## O que verificar

1. Um caminho em que o `wait`/`collect` apontam para relatórios diferentes,
   ou em que uma emenda falha deixa o destino num relatório que não virá.
2. Cópia estável presente enquanto a tarefa/emenda está pendente (falso
   "concluído").
3. Portabilidade: qualquer dependência de symlink, `rename` sobre arquivo
   aberto, separador de caminho.
4. `--for`: um nome que casa sidecar de outro agente, ou um autor liberado
   aceito com metadados ambíguos (enfraquece o modo estrito).
5. Texto novo da SKILL.md bate com o código.

## Checks you may run

Leitura, `git diff/log/show`; `node --test`/`bun test` em
`skills/herdr-soho/scripts/test/`; `run-tests.sh --env outside <suite>`;
sondas em `/tmp` com `HOME` temporário. Antes de afirmar que algo falha,
rode e cite a saída.

## Forbidden

- Editar qualquer arquivo. Revisor é somente leitura.
- Nenhum comando git que escreva. No commit, push, tag, or PR. The
  orchestrator owns git.

## Expected result

Primeira linha exata `findings: N (P0 a, P1 b, P2 c, P3 d) | verdict: pass|fail`,
depois um achado por item com severidade, `arquivo:linha`, cenário,
evidência colada e correção sugerida; `[done]`/`[partial]` por ponto.

## Report

Relatório em Markdown no caminho do contrato, pt-BR.
