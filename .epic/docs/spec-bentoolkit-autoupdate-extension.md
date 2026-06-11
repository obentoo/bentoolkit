# Spec — Extensão do `bentoolkit` autoupdate (transform / select / script)

**Data:** 2026-06-02 (rev. pós-revisão de código)
**Workspace alvo:** `~/Projetos/Pessoais/bentoo/bentoolkit/` (repo Go, NÃO o overlay)
**Objetivo:** dar ao parser do autoupdate três capacidades que faltam, para
registrar os pacotes hoje em SKIP por limitação de parsing (não por falta de fonte).

> **Changelog desta revisão (correções aplicadas após confronto com o código):**
> 1. `ValidatePackageConfig` (`config.go:115`) precisa ser atualizada — senão
>    `parser="script"` cai no `default → ErrInvalidParserType` e todo config
>    `script`/`select` é rejeitado antes de chegar ao parser. (§5.5, nova)
> 2. Cap. 3 passa a usar **`playwright-go`** (decisão do dono). Resolve de graça o
>    problema de Promises async (auto-await), que o `chromedp.Evaluate` cru NÃO
>    fazia. (§5 reescrita)
> 3. Cap. 2 **reusa `version_history.go`** (hoje código órfão) em vez de um
>    `ParseAll` paralelo. Reconcilia a sintaxe JSON (`[*]`, não `[]`) e o
>    `MaxVersionHistoryLimit=10` (que truncaria listas crescentes → `max` errado). (§4 reescrita)
> 4. Ordem `transform`×`select` agora é explícita: `extrair candidatos → transform
>    por item → selecionar`. Necessário porque `7.1.2-24` é inválido p/
>    `IsValidVersion` antes do transform. (§3 + §4)
> 5. `parser="script"` desvia de `fetchAndParse`, logo `transform`/`select` do
>    TOML são ignorados nesse ramo — agora declarado/validado. (§5 + §8)
> 6. Notas menores: limite parametrizável, evaluator testável, sort JS robusto,
>    requisito de browser em CI/cron. (§4, §5, §8)

---

## 1. Por que (as 3 limitações)

O pipeline atual é: `1 fetch → 1 regex/json-path → grupo 1 do PRIMEIRO match →
compara por igualdade Gentoo`. Os SKIP restantes quebram em 3 pontos:

| Capacidade | Resolve | Caso |
|---|---|---|
| **1. `transform`** (reescrever a string extraída) | separador `-`↔`.`/`_` | imagemagick (`7.1.2-24`→`7.1.2.24`), godot (`-beta`→`_beta`) |
| **2. `select = "max"`** (escolher a maior de N matches, não a 1ª) | listas crescentes | gn (`0.2122`→`0.2374`), libreoffice (dir mais novo) |
| **3. parser `script`** (executar JS num navegador headless) | multi-step / SPA / lógica arbitrária | libreoffice 4-seg, e qualquer caso futuro |

> `myspell-hu` foi re-versionado no overlay (`PV=LO_VER=26.2.3.2`) e passa a
> pertencer ao "grupo LibreOffice": resolve-se junto com `libreoffice` quando
> `select`+`script` existirem.

---

## 2. Mapa de arquivos do bentoolkit (pontos de extensão)

| # | Arquivo:linha | Papel |
|---|---|---|
| Struct de config | `internal/autoupdate/config.go:33-68` | `PackageConfig` (campos TOML) |
| **Validação** | `internal/autoupdate/config.go:115-159` | `ValidatePackageConfig` (`switch cfg.Parser`, default→erro) — **PRECISA atualizar** |
| Dispatch de parser | `internal/autoupdate/parser.go:310-328` | `NewParserFromConfig` (`switch cfg.Parser`) |
| Interface | `internal/autoupdate/parser.go:31-35` | `Parser { Parse([]byte) (string,error) }` |
| Regex | `internal/autoupdate/parser.go:246-280` | `RegexParser.Parse` (usa `FindSubmatch` na :268) |
| JSON | `internal/autoupdate/parser.go:44-70` | `JSONParser.Parse` (`parseJSONPath`, suporta `[N]`) |
| **Extração de listas (órfã)** | `internal/autoupdate/version_history.go:1-313` | `VersionHistoryExtractor` (JSON `[*]`, CSS, XPath). **Só usada em testes** — reusar p/ Cap. 2 |
| Fetch+parse | `internal/autoupdate/checker.go:614` | `fetchAndParse(url,parser,path,pattern,selector,xpath)` |
| Orquestrador | `internal/autoupdate/checker.go:570` | `fetchUpstreamVersion` (primary→fallback→llm) |
| Compare | `internal/autoupdate/checker.go:549-556` | `compareVersions` usa `ebuild.CompareVersions` |
| vercmp | `internal/common/ebuild/version.go:107-145` | `CompareVersions(a,b) int`, `IsValidVersion(v) bool` |
| regex de versão válida | `internal/common/ebuild/version.go:92` | `^[0-9]+(\.[0-9]+)*[a-z]?(_(alpha\|beta\|pre\|rc\|p)[0-9]*)*(-r[0-9]+)?$` |
| HTTP | `internal/autoupdate/checker.go:659-710` | `fetchContent` (rate-limit, retry, 10 MiB cap) |
| sink de warn | `internal/autoupdate/header_allowlist.go:53` | `var warnLogf = logger.Warn` (mockável em teste) |
| Deps | `go.mod` (go 1.25.10) | sem browser headless hoje |

---

## 3. Capacidade 1 — `transform` (regex-replace pós-extração)

### Config (`config.go`, após `VersionsSelector`)
```go
// Transform applies ordered regex substitutions to the extracted version,
// e.g. [["-", "."]] turns "7.1.2-24" into "7.1.2.24". Each rule is [regex, repl];
// repl follows regexp.ReplaceAllString semantics ($1 etc.). Rules run in order.
Transform [][]string `toml:"transform,omitempty"`
```

### Aplicação
`transform` roda **uma vez por candidato**, ANTES da seleção e ANTES de
`compareVersions`. No caminho de valor único (`select` ausente) isso é logo após
`parser.Parse`; no caminho `select` é por item da lista, antes de `selectVersion`
(ver §4 — a ordem importa: `7.1.2-24` é inválido p/ `IsValidVersion`).

```go
func applyTransforms(v string, rules [][]string) string {
    for _, r := range rules {
        if len(r) != 2 { continue }
        re, err := regexp.Compile(r[0])
        if err != nil { warnLogf("transform: bad regex %q: %v", r[0], err); continue }
        v = re.ReplaceAllString(v, r[1])
    }
    return v
}
```
Uso no `fetchAndParse` refatorado (recebendo `*PackageConfig`):
```go
version, err := parser.Parse(content)
if err != nil { return "", fmt.Errorf("failed to parse version: %w", err) }
version = applyTransforms(version, cfg.Transform)   // <-- single-value path
return version, nil
```
> ATENÇÃO: `fetchAndParse` hoje recebe campos soltos (`url,parser,…`), não o
> `*PackageConfig`. Passe `cfg` inteiro (refactor pequeno). Isso também é
> pré-requisito para `select` (precisa de `cfg.Select`) e mantém o ramo de
> fallback (`fetchUpstreamVersion`, `checker.go:581`) coerente.

### TOML resultante
```toml
["media-gfx/imagemagick"]
url = "https://api.github.com/repos/ImageMagick/ImageMagick/tags"
parser = "json"
path = "[0].name"
transform = [["-", "."]]            # 7.1.2-24 → 7.1.2.24
headers = { "User-Agent" = "bentoo-autoupdate" }

["dev-games/godot"]
url = "https://godotengine.org/download/archive/"
parser = "regex"
pattern = '(\d+\.\d+(?:\.\d+)?-(?:stable|beta\d*|rc\d*|dev\d*))'  # 1º item da archive
transform = [["-stable", ""], ["-beta", "_beta"], ["-rc", "_rc"], ["-dev", "_alpha"]]
# saída p/ godot vira _beta/_rc/_alpha → casa a regex de IsValidVersion (version.go:92)
```

---

## 4. Capacidade 2 — `select = "first" | "max" | "last"` (reusa `version_history.go`)

### Config
```go
// Select chooses which match to return when several are present.
// "" / "first" = current behavior; "max" = highest Gentoo version; "last" = last match.
Select string `toml:"select,omitempty"`
```

### Decisão: reaproveitar a infra de extração de listas existente
`version_history.go` já tem `JSONVersionHistoryExtractor` (path `[*].field`),
`HTMLVersionHistoryExtractor` (CSS) e `XPathVersionHistoryExtractor` — hoje **código
órfão** (usado só em testes). Em vez de criar um `ParseAll` paralelo (que duplicaria
a lógica e introduziria uma 2ª sintaxe JSON `[]` ≠ a existente `[*]`), `select`
passa a consumir essa infra. Trabalho:

1. **Parametrizar o limite** (hoje `const MaxVersionHistoryLimit = 10` trunca a
   lista — fatal p/ `select="max"` quando a página lista em ordem crescente e a
   maior cai além do 10º item, como gn). Adicionar campo `Limit int` aos
   extractors, com convenção retro-compatível:
   - `Limit == 0` → default `MaxVersionHistoryLimit` (preserva testes atuais);
   - `Limit < 0` → ilimitado (usado pelo caminho `select`);
   - `Limit > 0` → esse valor.
2. **Adicionar `RegexVersionHistoryExtractor`** (faltava o formato regex):
```go
type RegexVersionHistoryExtractor struct {
    Pattern  string
    Limit    int
    compiled *regexp.Regexp
}
func (e *RegexVersionHistoryExtractor) ExtractVersions(content []byte) ([]string, error) {
    if e.compiled == nil {                          // lazy-compile (evita panic)
        re, err := regexp.Compile(e.Pattern)
        if err != nil { return nil, fmt.Errorf("%w: %v", ErrInvalidRegexPattern, err) }
        if re.NumSubexp() < 1 { return nil, ErrNoCaptureGroup }
        e.compiled = re
    }
    all := e.compiled.FindAllSubmatch(content, -1)
    out := make([]string, 0, len(all))
    lim := e.Limit; if lim == 0 { lim = MaxVersionHistoryLimit }
    for _, m := range all {
        if len(m) >= 2 && len(m[1]) > 0 {
            out = append(out, string(m[1]))
            if lim > 0 && len(out) >= lim { break }
        }
    }
    if len(out) == 0 { return nil, ErrRegexNoMatch }
    return out, nil
}
```
3. Um seletor de extractor por `cfg.Parser` (json→`[*]` sobre `cfg.Path`,
   regex→`cfg.Pattern`, html→`cfg.Selector`/`XPath`), todos com `Limit = -1`
   quando vindos do `select`.

### Seleção + ordem com `transform` (`checker.go`, em `fetchAndParse`)
Quando `cfg.Select != "" && cfg.Select != "first"`: extrair TODOS os candidatos,
aplicar `transform` **por item**, então selecionar.
```go
import "github.com/obentoo/bentoolkit/internal/common/ebuild"

func selectVersion(cands []string, transform [][]string, mode string) string {
    best := ""
    for _, c := range cands {
        c = applyTransforms(strings.TrimSpace(c), transform)   // transform ANTES de validar
        cc := stripVersionPrefix(c)
        if !ebuild.IsValidVersion(cc) { continue }
        switch mode {
        case "last": best = cc
        case "max":
            if best == "" || ebuild.CompareVersions(cc, best) > 0 { best = cc }
        }
    }
    return best
}
```
> Se `select` for pedido mas o parser não tiver extractor de lista (não deve
> ocorrer após o passo 2/3, mas defensivo), caia para o comportamento `first`
> com `warnLogf`. `select` **não** se aplica ao ramo `parser="script"` (§5/§8).

### TOML resultante
```toml
["dev-build/gn"]
url = "https://deps.gentoo.zip/dev-build/gn/"
parser = "regex"
pattern = 'gn-([0-9][0-9.]*)\.tar\.xz'
select = "max"                      # escolhe 0.2374, não 0.2122 (lista crescente, sem cap-10)
```

---

## 5. Capacidade 3 — parser `script` (navegador headless via `playwright-go`)

Para casos que exigem **interação / multi-step / DOM pós-JS** (libreoffice 4-seg,
SPAs futuras). Um script JS recebe a página e devolve a string de versão final
(já no formato Gentoo — pode fazer transform e max dentro do próprio script).

### Biblioteca: `playwright-go` (decisão do dono)
- `github.com/playwright-community/playwright-go`.
- **Vantagem decisiva:** `page.Evaluate(expr)` faz **auto-await de Promises** — um
  script `(async () => {...})()` resolve corretamente para a string. (Com
  `chromedp.Evaluate` cru isso NÃO acontecia: retornaria o objeto Promise não
  resolvido, a menos de `WithAwaitPromise(true)` explícito.)
- **Custo:** exige os browsers do Playwright instalados
  (`playwright install chromium`, ou `playwright.Install()` no setup).
  Documentar e falhar com mensagem clara se ausente.
- **Opt-in via build-tag (decisão de implementação):** o evaluator real
  (`playwrightEvaluator`) fica em `script_evaluator_playwright.go` sob
  `//go:build playwright`. O build padrão **não** linka playwright-go nem os
  browsers; `parser="script"` então falha com `ErrScriptSupportNotBuilt`
  ("rebuild com `-tags playwright`"). Isso mantém o binário/CI padrão leve e a
  dependência verdadeiramente opcional. `go.mod` lista playwright-go como
  `// indirect` (nenhum arquivo do build padrão o importa); **`go mod tidy`
  precisa do `-tags playwright`** para não podá-lo.
- **Ciclo de vida (decisão de implementação):** um evaluator é criado **por
  chamada** (`newLiveEvaluator` lança Chromium; `Close()` derruba browser+driver
  via `io.Closer` no `defer`). O singleton/reuso entre pacotes fica como
  otimização futura — o nº de pacotes `script` é mínimo (grupo LibreOffice), e
  isolar por chamada evita risco de estado compartilhado sob a concorrência do
  `CheckAll`.

### Config
```go
// Script is a JS expression/IIFE evaluated in the page; its string result is the
// version. Inline, or "@file.js" to load from .autoupdate/scripts/<file>.
Script string `toml:"script,omitempty"`
```

### Dispatch
`parser="script"` NÃO usa `fetchContent`+`Parse` (precisa do DOM renderizado e
navega ele próprio). A ramificação é em `fetchUpstreamVersion` (`checker.go:570`),
ANTES da chamada a `fetchAndParse`:
```go
if cfg.Parser == "script" {
    return c.parseLive(cfg)   // mantém rate-limiter por host + opTimeout; pula fetchContent
}
```
> `NewParserFromConfig` não retorna um `Parser` para `script` (a interface
> `Parse([]byte)` não serve — não há `[]byte` pré-buscado). O ScriptParser é
> acionado pelo orquestrador, não pelo dispatch do parser.

### Parser (novo arquivo `internal/autoupdate/script_parser.go`) — evaluator testável
Abstrair o motor headless atrás de uma interface para testar sem browser real:
```go
// liveEvaluator renders a URL and evaluates a JS expression against the live DOM.
type liveEvaluator interface {
    Evaluate(ctx context.Context, url, script string, headers map[string]string) (string, error)
}

type ScriptParser struct {
    URL, Script string
    Headers     map[string]string
    eval        liveEvaluator   // default: playwrightEvaluator; testes injetam um fake
}

func (p *ScriptParser) ParseLive(ctx context.Context) (string, error) {
    out, err := p.eval.Evaluate(ctx, p.URL, p.Script, p.Headers)
    return strings.TrimSpace(out), err
}
```
Implementação real (`playwrightEvaluator`), esboço:
```go
func (e *playwrightEvaluator) Evaluate(ctx context.Context, url, script string,
    headers map[string]string) (string, error) {
    page, err := e.browser.NewPage()           // browser é o singleton reusado
    if err != nil { return "", err }
    defer page.Close()
    page.SetDefaultTimeout(float64(e.opTimeout.Milliseconds()))  // honra opTimeout
    if len(headers) > 0 { page.SetExtraHTTPHeaders(headers) }
    if _, err := page.Goto(url); err != nil { return "", err }
    res, err := page.Evaluate(script)           // auto-await de Promise
    if err != nil { return "", err }
    s, ok := res.(string)
    if !ok { return "", fmt.Errorf("script result is not a string: %T", res) }
    return s, nil
}
```
> Cancelamento via `ctx`: `playwright-go` não é nativamente context-aware em
> `Goto/Evaluate`; combine `SetDefaultTimeout` (deadline) com um goroutine +
> `select { case <-ctx.Done(): page.Close() }` para abortar em SIGINT.

### TOML + script de exemplo (libreoffice 4-seg, multi-step)
```toml
["app-office/libreoffice"]
parser = "script"
url = "https://download.documentfoundation.org/libreoffice/src/"
script = "@libreoffice.js"
```
`.autoupdate/scripts/libreoffice.js` (executado na página de `src/`):
```js
// 1) acha o diretório de versão mais novo; 2) entra; 3) extrai o tarball 4-seg.
(async () => {
  const dirs = [...document.querySelectorAll('a')]
    .map(a => a.textContent.replace('/', ''))
    .filter(t => /^\d+\.\d+\.\d+$/.test(t))
    .sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));  // robusto p/ multi-seg
  const newest = dirs[dirs.length - 1];
  const html = await (await fetch(location.href + newest + '/')).text();
  return (html.match(/libreoffice-[\w-]*?(\d+\.\d+\.\d+\.\d+)\.tar\.xz/) || [])[1] || '';
})()
```
> O mesmo registro serve `libreoffice-l10n` e `myspell-hu` (todos seguem o LO).

---

## 5.5. Validação (`config.go`, `ValidatePackageConfig` :115) — **OBRIGATÓRIO**

`ValidatePackageConfig` tem um `switch cfg.Parser` com `default → ErrInvalidParserType`.
Adicionar `parser="script"` ao dispatch SEM tocar aqui faz todo config `script`
ser rejeitado na validação. Mudanças:

1. Novo `case "script"`: exigir `cfg.Script != ""` (e `URL != ""`, já coberto).
2. Validar `cfg.Select`: aceitar apenas `"" | "first" | "max" | "last"`.
3. Para `select="max"/"last"`: o parser precisa conseguir extrair lista
   (json/regex/html) — avisar/erro se combinado com `script`.
4. `transform`: validar que cada regra tem `len == 2` e que `r[0]` compila
   (`regexp.Compile`); avisar e ignorar regras inválidas (consistente com
   `applyTransforms`), ou erro hard — escolher e documentar.
5. Atualizar a mensagem de `ErrInvalidParserType` (`config.go:18`):
   `"must be 'json', 'regex', 'html', or 'script'"`.

---

## 6. Mapa de resolução por pacote (após a extensão)

| Pacote | Capacidade | Registro (resumo) |
|---|---|---|
| media-gfx/imagemagick | transform | github tags `[0].name` + `transform=[["-","."]]` |
| dev-games/godot | transform | archive regex (1º item) + `transform` `-beta`→`_beta` |
| dev-build/gn | select=max | `deps.gentoo.zip` regex `gn-(…)\.tar` + `select="max"` (lista crescente) |
| app-office/libreoffice | script | `src/` + `libreoffice.js` (max dir → 4-seg) |
| app-office/libreoffice-l10n | script | idem libreoffice |
| app-dicts/myspell-hu | script | idem libreoffice (PV já = LO_VER) |

Sobram sem fonte (não dependem da extensão, faltam dados upstream):
`crossover-bin`, `filezilla-pro`, `warsaw`, `antigravity-bin`.

---

## 7. Plano de implementação

1. `config.go`: +`Transform`, `Select`, `Script` (+ doc-comments) **e** atualizar
   `ValidatePackageConfig` + mensagem `ErrInvalidParserType` (§5.5).
2. `version_history.go`: parametrizar `Limit` nos extractors (retro-compat:
   `0`=default 10, `<0`=ilimitado); adicionar `RegexVersionHistoryExtractor`;
   helper que escolhe extractor por `cfg.Parser` com `Limit=-1` p/ `select`.
3. `parser.go` / `checker.go`: `applyTransforms`; refactor `fetchAndParse` p/
   receber `*PackageConfig`; ramificar `select` (extractor de lista +
   `selectVersion` com transform-por-item); ramificar `parser=="script"` em
   `fetchUpstreamVersion` p/ `ParseLive`.
4. `script_parser.go` (novo): `liveEvaluator` + `playwrightEvaluator`
   (singleton do driver/browser) + `ScriptParser`. `go.mod`: +`playwright-go`.
5. Testes (`*_test.go`, property-based já usa gopter):
   - transform: `7.1.2-24`+`[["-","."]]` == `7.1.2.24`; godot betas → `_beta`/`_rc`/`_alpha`.
   - select=max: lista `gn-0.2122…0.2374` → `0.2374` (e confirmar que SEM cap-10).
   - select: ordem `transform`→validar→`max` (item inválido pré-transform é
     incluído pós-transform).
   - script: injetar um `liveEvaluator` fake (sem browser) p/ testar
     `ParseLive`/seleção; 1 teste de integração opcional, *build-tagged*, com
     `playwright` real sobre página `httptest`.
   - validação: `parser="script"` sem `Script`; `select` inválido;
     `select` + `script` (deve avisar/erro).
6. Validar no overlay: `bentoo overlay autoupdate --check <pkg> --force` para os 6.

## 8. Considerações

- **Lazy/opt-in:** playwright só é acionado por `parser="script"`; os 251 registros
  HTTP continuam sem tocar o navegador. Documente o requisito de browsers do
  Playwright instalados (ou falhe com mensagem clara se ausente).
- **CI / cron headless:** o teste de integração do §7.5 e qualquer execução
  agendada precisam dos browsers instalados no runner; mantenha-o atrás de
  build-tag e/ou skip quando `playwright.Install` não estiver disponível, para não
  quebrar o CI padrão.
- **Timeout/limites:** reutilize `opTimeout` (30s) e o rate-limiter por host no
  ramo `script` (a ramificação fica em `fetchUpstreamVersion`, que já roda sob o
  contexto do Checker). Cap de output do script (defensivo).
- **Segurança:** o `script` roda JS arbitrário num navegador — ele vem do
  `packages.toml` do próprio overlay (confiável). Ainda assim, rode em modo
  headless/sandbox e sem credenciais.
- **`transform` × `select` × `compareVersions`:** `transform` roda por candidato,
  ANTES de `selectVersion`/`compareVersions` (que ainda fazem `stripVersionPrefix`).
  Ordene as regras do mais específico para o mais geral (`-beta`→`_beta` antes de
  um eventual `-`→`.`). Sem o transform-antes, `7.1.2-24` reprovaria em
  `IsValidVersion` e o candidato seria descartado.
- **`script` ignora `transform`/`select` do TOML:** esse ramo desvia de
  `fetchAndParse`; toda a normalização é responsabilidade do próprio JS. A
  validação (§5.5) deve avisar se `transform`/`select` forem combinados com
  `parser="script"`, para não dar falsa impressão de que se aplicam.
