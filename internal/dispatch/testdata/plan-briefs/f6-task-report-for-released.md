# Brief — F6: relatório estável por tarefa (#8) e `--for` de autor liberado (#10)

Role: implementer · Agent: build · Report language: pt-BR

Worktree própria (o orquestrador informa o caminho na mensagem de envio);
caminhos relativos à raiz dela. O CLI se chama `herdr-soho`
(`skills/herdr-soho/scripts/herdr-soho`).

## Goal

1. (#8) Cada tarefa tem um caminho estável para o relatório autoritativo,
   válido através de emendas, sem perder os relatórios anteriores; uma
   falha de envio nunca troca o destino para um relatório que não virá.
2. (#10) `dispatch --for <agente>` continua comparando famílias quando o
   autor já saiu do roster, usando só os metadados persistidos das tarefas
   dele; ambiguidade ou ausência falha com diagnóstico claro.

## Decisions already made

### Item 1 — relatório estável por tarefa (#8), sem symlink

Tem de funcionar igual em macOS, Linux e Windows sem privilégio de
symlink: **nenhum symlink**, só arquivos regulares e `rename`.

- Os relatórios versionados continuam iguais: cada tentativa (dispatch ou
  `--amend`) tem o próprio `<agent>-<ts>[-N].md`, e `last-report-<agent>`
  aponta para a tentativa atual (comportamento de hoje). O prompt de
  emenda (`composeAmendment`) não muda.
- **Ponteiro da tarefa** `<state>/task-report-<agent>.json`, escrito
  atomicamente (temporário no mesmo diretório + `rename`), formato exato:
  `{"version":1,"task_report":"<abs>","current":"<abs>","history":["<abs>",…]}`
  (`history` do mais antigo ao mais novo).
- **Cópia estável** `task_report` = `<state>/reports/<stem da tentativa que abriu a tarefa>.current.md`
  (sempre no diretório de estado, mesmo quando o relatório versionado está
  no roteamento em `$TMPDIR`). Arquivo regular.
- **Função única** exportada em `lib/dispatch.mjs` (ou módulo novo
  `lib/taskreport.mjs`): `syncTaskReport(sd, agent)` — lê o ponteiro; se
  `current` existe e não está vazio, grava a cópia estável com o mesmo
  conteúdo (atômico; não regrava se o conteúdo já é igual); senão, remove a
  cópia estável se existir. Sem ponteiro: não faz nada. Retorna o caminho
  da cópia estável ou `null`.
- **Semântica:**
  1. *Dispatch comum (nova tarefa):* antes do envio (onde `last-report` é
     escrito hoje), guarde o conteúdo anterior de `last-report-<agent>` e do
     ponteiro (ou "ausente"); grave o ponteiro novo
     `{task_report: <novo .current.md>, current: <relatório novo>, history: []}`
     e chame `syncTaskReport` (a cópia não existe: o relatório ainda não).
  2. *`--amend`:* antes do envio, guarde os mesmos dois estados; grave o
     ponteiro com o mesmo `task_report`, `current` = relatório da emenda e
     o `current` anterior acrescentado a `history`; chame `syncTaskReport`
     (a cópia é removida até a emenda concluir). `--amend` sem ponteiro
     (tarefa aberta antes desta versão): crie o ponteiro como no caso 1,
     com o `last-report` anterior em `history`.
  3. *Falha de envio* (o transporte não aceitou: o caminho que marca o
     sidecar `failed` ou sai por erro antes de `accepted`): restaure
     `last-report-<agent>` e o ponteiro exatamente como estavam (remova o
     que não existia) e chame `syncTaskReport` — a cópia volta a refletir o
     `current` restaurado. `not-received` (o transporte aceitou) mantém o
     destino novo.
  4. *Conclusão:* `wait`, ao emitir `done` para um agente cujo ponteiro tem
     `current` igual ao relatório concluído, chama `syncTaskReport` e
     acrescenta `task_report` ao JSON logo depois de `report` (ou de
     `settled_report`, quando houver). `collect <agent>` chama
     `syncTaskReport` antes de imprimir e, quando há ponteiro, imprime logo
     depois de `<!-- report: <path> -->` a linha
     `<!-- task report: <task_report> -->`. `status` não escreve nada.
- JSON do `dispatch` (normal e `--amend`): chave `task_report` logo
  **depois** de `report`, antes de `settled_report`/`report_exists`.
  Nenhuma outra chave muda de posição.
- `clean`, `stats`, espelhamento do `wait`: um `*.current.md` e o
  `task-report-*.json` não são relatório nem par. Confira cada
  `readdirSync` de `reports/`, `briefs/` e do roteamento e diga no
  relatório quais olhou e o que mudou.

### Item 2 — `--for` com autor liberado (#10)

- Só `forSpecFamily` (`dispatch.mjs:112-124`) muda. Ordem: (1) roster
  vivo, como hoje; (2) nome de família; (3) kind de família fixa;
  (4) **novo**: metadados de tarefas liberadas; (5) erro.
- Metadados = sidecars `<nome>-<AAAAMMDDTHHMMSS>[-N].dispatch.json` com
  `"submission":"accepted"`, nos diretórios que o `stats` varre
  (`<state>/briefs/` e `$TMPDIR/herdr-soho/<workspace>/reports/`); exporte e
  reutilize o varredor de `lib/commands/stats.mjs` (`collectPrompts` e o
  leitor de sidecar), sem mudar o comportamento do `stats`. Nome do agente
  = prefixo antes de `-<timestamp>`, igualdade exata (`build` não casa
  `build-2-20260927T101010`).
- Família de cada sidecar: `agentFamily(kind, model)` (`lib/kinds.mjs:57`):
  - todas iguais e conhecidas → essa família;
  - nenhum sidecar aceito → `DieError` exit 2:
    `dispatch: --for '<spec>': not an agent in the roster, a family (anthropic|openai|xai|google), a kind with a fixed family, or an agent with an accepted dispatch recorded in this workspace`
  - famílias diferentes, ou alguma `unknown` → `DieError` exit 2:
    `dispatch: --for '<spec>': the recorded dispatches of this released agent do not agree on one known model family (<fam> ×<n>, …); pass the family instead (anthropic|openai|xai|google)`
    (lista ordenada por nome da família; `unknown` conta como família).
- Other configuration and dispatch options remain unchanged.

## Expected result

`dispatch` devolve `task_report`; durante a tarefa a cópia estável não
existe; depois do `wait`/`collect` ela tem o conteúdo do relatório atual;
duas emendas seguidas deixam o ponteiro com dois itens em `history` e a
cópia com o relatório da segunda; uma falha de envio deixa `last-report`,
ponteiro e cópia como estavam; `--for` resolve autor liberado com
metadados coerentes.

## Acceptance criteria

1. `skills/herdr-soho/scripts/test/task-report.test.mjs` (fakes existentes
   de `dispatch.test.mjs`; `spawnSync` com `timeout`), cobrindo cada item
   da semântica 1–4: cópia ausente durante a tarefa; `wait` done → cópia
   igual ao relatório e `task_report` no JSON; duas emendas → `history` com
   dois caminhos, os três versionados no disco, cópia com a segunda depois
   do `collect`; `wait` e `collect` resolvem o mesmo relatório; envio
   falho (herdr falso recusando `agent prompt`) → `last-report`, ponteiro e
   cópia byte a byte iguais aos de antes; `clean` e `stats` ignoram
   `*.current.md` e `task-report-*.json`; a cópia é arquivo regular
   (`lstatSync(...).isSymbolicLink() === false`).
   **Fallback sem symlink:** o mesmo fluxo dispatch → emenda → done →
   collect roda num processo em que `fs.symlinkSync` e
   `fs.promises.symlink` lançam `EPERM` (como num Windows sem privilégio)
   e produz o mesmo resultado. O CLI é iniciado pelo mesmo runtime que roda
   o teste: sob Node, `node --import <stub>.mjs …/herdr-soho.mjs`; sob Bun
   (`process.versions.bun`), `bun --preload <stub>.mjs …/herdr-soho.mjs`.
   O teste passa em `node --test` **e** em `bun test`.
2. `skills/herdr-soho/scripts/test/for-released.test.mjs`: autor liberado
   com sidecars coerentes participa da checagem (revisor da mesma família →
   recusa como hoje); sem sidecar aceito → primeira mensagem; divergente ou
   `unknown` → segunda; autor vivo no roster vence; `build` não casa
   `build-2`.
3. Cada teste com `// Mutation captured: …` executado (cite comando e saída).
4. `node --test` e `bun test` de
   `task-report.test.mjs for-released.test.mjs dispatch.test.mjs collect.test.mjs stats.test.mjs wait.test.mjs parity-dispatch.test.mjs parity-wait.test.mjs`
   → 0 fail. Os goldens `parity-dispatch` e `parity-wait` podem mudar **só** pela
   chave `task_report`; regrave apenas eles com
   `HERDR_SOHO_GOLDEN=update node --test skills/herdr-soho/scripts/test/parity-dispatch.test.mjs skills/herdr-soho/scripts/test/parity-wait.test.mjs`
   e cole um trecho do diff que prove isso. Qualquer outra mudança de
   golden: pare e marque `[partial]`.

## When the brief does not decide

Não escolha. Marque `[partial]`, liste a lacuna e as opções em "Open
questions" e siga. Nunca invente nomes, flags ou requisitos.

## Owned files

- `skills/herdr-soho/scripts/lib/dispatch.mjs` — `forSpecFamily` e a função do dispatch
- `skills/herdr-soho/scripts/lib/commands/collect.mjs`, `lib/commands/clean.mjs`
- `skills/herdr-soho/scripts/lib/commands/stats.mjs` — só exportar/filtrar, sem mudar a saída
- `skills/herdr-soho/scripts/lib/taskreport.mjs` — novo, se preferir módulo próprio
- `skills/herdr-soho/scripts/lib/wait.mjs` — só a sincronização no `done` e o filtro de varredura
- `skills/herdr-soho/scripts/test/task-report.test.mjs`, `for-released.test.mjs` e o pré-carregamento de teste que desliga symlink — novos
- `skills/herdr-soho/scripts/test/golden/parity-dispatch.json`, `parity-wait.json` — só pelo critério 4

## Forbidden

- `lib/commands/status.mjs`, `lib/commands/lint.mjs`, `lib/commands/mutation-guard.mjs`,
  papéis, templates, `SKILL.md`, README, docs (o orquestrador documenta —
  proponha o texto no relatório).
- Outros goldens.
- Nenhum comando git que escreva. No commit, push, tag, or PR. The
  orchestrator owns git.

## Sources (local paths, read before editing)

- `skills/herdr-soho/scripts/lib/dispatch.mjs:100-125`, `:616-660`, `:880-1180`
- `skills/herdr-soho/scripts/lib/commands/stats.mjs:1-230`
- `skills/herdr-soho/scripts/lib/commands/collect.mjs`, `clean.mjs`
- `skills/herdr-soho/scripts/lib/wait.mjs:470-520` (espelhamento)
- `skills/herdr-soho/scripts/lib/kinds.mjs:30-70`
- `skills/herdr-soho/scripts/test/dispatch.test.mjs` — fakes e padrão

## Project rules that apply

- Código e mensagens em inglês; comentários na densidade dos vizinhos.
- Escrita atômica (temporário no mesmo diretório + rename); nada é apagado
  além do que o item 1 manda restaurar.
- Teste de comportamento observável; mutação nomeada e executada.

## Checks you may run

Os do critério 4 e `node --test <um arquivo>` enquanto itera. Não rode a
suíte completa nem a matriz; não rode formatador.

## Non-goals

Mudar o formato dos relatórios versionados, o prompt de emenda ou a
checagem de família para autores vivos.

## Report

Relatório em Markdown no caminho do contrato, por item `[done]` /
`[partial]` / `[skipped]` + motivo, com saídas coladas (nunca redigitadas),
as mutações executadas e uma seção "Texto proposto para SKILL.md".
