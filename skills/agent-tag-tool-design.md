# Agent Tag Tool 设计思路

本文描述如何基于 EhTagTranslation 的 `db.html.json` 返回数据，构建一个供 agent 使用的标签索引与查询工具。内容为伪代码级设计，不绑定具体语言或框架。

## 目标

构建一组 `agentTools`，支持：

- 加载并索引 EhTagTranslation 标签数据库。
- 根据中文名、英文 key、命名空间、简介检索标签。
- 将中文标签解析为 E-Hentai 搜索表达式，例如 `中文` -> `language:chinese`。
- 支持 `female:xxx`、`语言:中文`、`-tag`、`~tag`、带引号短语等搜索输入。
- 返回可用于搜索漫画 API 的结构化标签候选结果。

## 原始数据结构

`db.html.json` 的顶层结构可抽象为：

```pseudo
Root {
  repo: string
  head: {
    sha: string
    committer: { when: string }
  }
  version: number
  data: NamespaceBlock[]
}

NamespaceBlock {
  namespace: string
  count: number
  data: Map<tagKey, RawTag>
}

RawTag {
  name: string   // HTML，例如 "<p>中文</p>"
  intro: string  // HTML
  links: string  // HTML
}
```

索引前需要把每个 `NamespaceBlock.data` 展平为统一的 `TagRecord`。

## 标准标签记录

```pseudo
TagRecord {
  namespace: string              // 原始命名空间，例如 language, female, artist
  key: string                    // E-Hentai 原始标签 key，例如 chinese
  namespaceZh: string?           // 中文命名空间，例如 语言
  tagName: string?               // 去除 HTML 后的中文标签名，例如 中文
  fullTagNameHtml: string?       // 原始 name HTML
  introText: string?             // 去除 HTML 后的简介文本
  introHtml: string?             // 原始 intro HTML
  linksHtml: string?             // 原始 links HTML
  namespaceWithKey: string       // `${namespace}:${key}`
}
```

## 命名空间映射

工具内置一份命名空间映射，用于支持英文命名空间、中文命名空间、缩写之间的互相识别。

```pseudo
NamespaceMeta {
  desc: string       // language
  zh: string         // 语言
  aliases: string[]  // ["lang", "语言"]
  weight: number
}

namespaceMap = {
  rows:      { zh: "内容索引", weight: 0 },
  reclass:   { zh: "重新分类", weight: 1 },
  language:  { zh: "语言", weight: 2 },
  group:     { zh: "团队", weight: 2.2 },
  artist:    { zh: "艺术家", weight: 2.5 },
  character: { zh: "角色", weight: 2.8 },
  parody:    { zh: "原作", weight: 3.3 },
  mixed:     { zh: "混合", weight: 8 },
  male:      { zh: "男性", weight: 8.5 },
  female:    { zh: "女性", weight: 9 },
  other:     { zh: "其他", weight: 10 }
}
```

权重用于无频率数据时的候选排序。权重越高，标签在自动补全中越靠前。

## 索引结构

推荐同时维护几类索引，兼顾精确查询、模糊搜索和高亮位置返回。

```pseudo
TagIndex {
  byNamespaceAndKey: Map<string, TagRecord>
  byKey: Map<string, TagRecord[]>
  byNamespace: Map<string, TagRecord[]>
  searchRows: SearchRow[]
  tagFrequency: Map<string, number>
  timestamp: string
  version: number
}

SearchRow {
  record: TagRecord
  keyLower: string
  tagNameLower: string
  namespaceLower: string
  namespaceZhLower: string
  introLower: string
}
```

索引键建议：

- `byNamespaceAndKey`: 使用 `${namespace}:${key}`，用于精确翻译。
- `byKey`: 使用 `key`，用于无命名空间查询。
- `byNamespace`: 使用 `namespace`，用于限定命名空间检索。
- `searchRows`: 线性扫描或交给全文索引，用于包含匹配。
- `tagFrequency`: 可选，用于按标签热度排序。

如果数据量较小，`searchRows` 线性扫描已经足够。若要优化，可将 `keyLower`、`tagNameLower`、`introLower` 建成 trigram、prefix map 或 SQLite FTS。

## 构建索引伪代码

```pseudo
function buildTagIndex(rawJson): TagIndex
  root = parseJson(rawJson)
  index = new TagIndex()
  index.timestamp = root.head.committer.when
  index.version = root.version

  for block in root.data:
    namespace = normalizeLower(block.namespace)
    namespaceZh = resolveNamespaceZh(namespace)

    for key, rawTag in block.data:
      tagName = stripHtml(rawTag.name)
      introText = stripHtml(rawTag.intro)

      record = TagRecord(
        namespace = namespace,
        key = normalizeLower(key),
        namespaceZh = namespaceZh,
        tagName = tagName,
        fullTagNameHtml = rawTag.name,
        introText = introText,
        introHtml = rawTag.intro,
        linksHtml = rawTag.links,
        namespaceWithKey = namespace + ":" + normalizeLower(key)
      )

      index.byNamespaceAndKey[record.namespaceWithKey] = record
      index.byKey[record.key].append(record)
      index.byNamespace[record.namespace].append(record)
      index.searchRows.append(makeSearchRow(record))

  return index
```

HTML 清理建议使用 HTML parser，不建议只用正则。若工具运行环境受限，可用保守的 `stripTags` 作为降级方案。

```pseudo
function stripHtml(html): string
  if html is null:
    return ""
  text = htmlParser.parse(html).textContent
  return decodeHtmlEntities(text).trim()
```

## 搜索输入解析

参考原项目的思路，输入可以拆成多个搜索片段。每个片段保留匹配区间，便于 agent 返回高亮信息或替换建议。

支持形式：

- `abc`
- `namespace:abc`
- `"ab cd"`
- `namespace:"ab cd"`
- `-abc`
- `~abc`
- `-namespace:abc`
- `~namespace:"ab cd"`

```pseudo
Token {
  raw: string
  operator: string?       // "-" 或 "~"
  namespaceInput: string?
  keyInput: string
  start: number
  end: number
}

function parseSearchText(searchText): Token[]
  pattern = /[-~]?(\S+?):"([^"]+)"?|[-~]?"([^"]+)"?|[-~]?(\S+?):(\S+)|[-~]?(\S+)/g
  tokens = []

  for match in regexFindAll(pattern, searchText.lowercase()):
    raw = match.fullText
    operator = raw startsWith "-" or "~" ? raw[0] : null
    namespaceInput = match.group(1) or match.group(4)
    keyInput = match.group(2) or match.group(3) or match.group(5) or match.group(6)
    tokens.append(Token(raw, operator, namespaceInput, keyInput, match.start, match.end))

  return tokens
```

## 合并搜索片段

原项目会从每个 token 开始，把后续 token 合并为一个搜索串，以支持用户正在输入多词标签的场景。

```pseudo
function buildSearchCandidates(tokens): CandidateInput[]
  candidates = []

  for i in range(0, tokens.length):
    merged = join tokens[i..end] with space
      if token.namespaceInput exists:
        token.namespaceInput + ":" + token.keyInput
      else:
        token.keyInput

    colonIndex = merged.indexOf(":")
    namespaceInput = colonIndex >= 0 ? merged.substring(0, colonIndex) : null
    keyInput = colonIndex >= 0 ? merged.substring(colonIndex + 1) : merged

    namespace = resolveNamespace(namespaceInput)
    candidates.append({
      namespace: namespace,
      key: keyInput,
      start: tokens[i].start,
      end: tokens[i].end,
      operator: tokens[i].operator
    })

  return candidates
```

如果 `keyInput` 只有 1 个英文字母或数字，可以跳过，避免过宽匹配。

## 检索逻辑

```pseudo
function searchTags(index, searchText, options): TagMatch[]
  tokens = parseSearchText(searchText)
  candidateInputs = buildSearchCandidates(tokens)
  results = []
  seen = Set<string>()

  for input in candidateInputs:
    if shouldSkip(input.key):
      continue

    if input.namespace exists:
      records = searchFullTags(index, input.namespace, input.key, options.includeIntro)
    else:
      records = searchAnyNamespace(index, input.key, options.includeIntro)

    matches = scoreRecords(index, records, input, options)

    for match in sortByScoreDesc(matches):
      id = match.record.namespaceWithKey
      if id not in seen:
        results.append(match)
        seen.add(id)

  return take(results, options.limit)
```

限定命名空间搜索：

```pseudo
function searchFullTags(index, namespaceInput, keyPattern, includeIntro): TagRecord[]
  namespace = resolveNamespace(namespaceInput)
  key = normalizeLower(keyPattern)

  return index.byNamespace[namespace].filter(row =>
    contains(row.keyLower, key) or
    contains(row.tagNameLower, key) or
    (includeIntro and contains(row.introLower, key))
  )
```

全命名空间搜索：

```pseudo
function searchAnyNamespace(index, pattern, includeIntro): TagRecord[]
  key = normalizeLower(pattern)

  return index.searchRows.filter(row =>
    contains(row.keyLower, key) or
    contains(row.tagNameLower, key) or
    (includeIntro and contains(row.introLower, key))
  ).map(row => row.record)
```

## 排序策略

### 优先策略：频率排序

如果有标签频率数据，按 `${namespace}:${key}` 查频率，频率越高越靠前。

```pseudo
function scoreByFrequency(record, index): number
  return index.tagFrequency[record.namespaceWithKey] or 0
```

### 降级策略：权重打分

没有频率数据时，使用命名空间权重、匹配长度和命中位置打分。

```pseudo
function scoreByText(record, input): number
  score = 0
  weight = namespaceMap[record.namespace].weight
  query = normalizeLower(input.key)

  keyIndex = record.keyLower.indexOf(query)
  if keyIndex >= 0:
    score += weight * (query.length + 1) / record.keyLower.length
    if keyIndex == 0:
      score *= 2

  tagNameIndex = record.tagNameLower.indexOf(query)
  if tagNameIndex >= 0:
    score += weight * (query.length + 1) / record.tagNameLower.length
    if tagNameIndex == 0:
      score *= 2

  introIndex = record.introLower.indexOf(query)
  if introIndex >= 0:
    score += weight * (query.length + 1) / record.introLower.length * 0.5

  return score
```

返回结果中建议包含命中区间：

```pseudo
TagMatch {
  searchText: string
  matchStart: number
  matchEnd: number
  operator: string?
  tag: TagRecord
  score: number
  namespaceMatch: Range?
  namespaceZhMatch: Range?
  keyMatch: Range?
  tagNameMatch: Range?
}
```

## 中文标签转搜索表达式

```pseudo
function resolveKeyword(index, keyword): ResolveResult
  matches = searchTags(index, keyword, { limit: 10, includeIntro: true })

  if matches is empty:
    return { source: "rawKeyword", keyword: keyword, candidates: [] }

  best = matches[0]
  return {
    source: "tagTranslation",
    keyword: best.tag.namespace + ":" + best.tag.key,
    candidates: matches
  }
```

如果用户输入包含多个词，agent 可以只替换最后一个正在编辑的 token，也可以返回候选让用户选择。

## agentTools 封装建议

将标签能力封装为 4 个工具：加载、查询、解析、翻译。

### `tagIndex.load`

加载本地或远程标签数据库，并构建内存索引。

```pseudo
tool tagIndex.load(input): output
input = {
  sourceUrl?: string,
  localPath?: string,
  cachePath?: string,
  forceRefresh?: boolean
}

output = {
  ok: boolean,
  version: number,
  timestamp: string,
  totalTags: number,
  source: "cache" | "local" | "remote",
  warnings: string[]
}
```

行为：

1. 如果已有内存索引且未要求刷新，直接返回。
2. 优先读 `localPath` 或 `cachePath`。
3. 若本地不存在，从 `sourceUrl` 下载。
4. 构建索引后写入缓存。
5. 不在日志中输出过长原始 JSON。

### `tagIndex.search`

搜索标签候选。

```pseudo
tool tagIndex.search(input): output
input = {
  query: string,
  limit?: number,
  namespace?: string,
  includeIntro?: boolean,
  useFrequency?: boolean
}

output = {
  query: string,
  matches: TagMatch[]
}
```

`matches` 中每项建议包含：

```pseudo
{
  namespace: string,
  key: string,
  namespaceZh: string?,
  tagName: string?,
  intro: string?,
  searchValue: string,       // `${namespace}:${key}`
  operator: string?,
  score: number,
  ranges: {
    key?: [start, end],
    tagName?: [start, end]
  }
}
```

### `tagIndex.resolveKeyword`

把用户输入解析为可用于漫画搜索的关键词。

```pseudo
tool tagIndex.resolveKeyword(input): output
input = {
  keyword: string,
  autoSelect?: boolean,
  limit?: number
}

output = {
  originalKeyword: string,
  resolvedKeyword: string,
  source: "tagTranslation" | "rawKeyword" | "ambiguous",
  selected?: TagMatch,
  candidates: TagMatch[],
  warnings: string[]
}
```

行为：

- 如果输入已有 `namespace:key`，直接返回 `rawKeyword`。
- 如果只有一个高置信候选且 `autoSelect=true`，返回该候选的 `searchValue`。
- 如果候选分数接近，返回 `ambiguous`，让 agent 询问用户选择。

### `tagIndex.translate`

把 E-Hentai 标签翻译为中文展示信息。

```pseudo
tool tagIndex.translate(input): output
input = {
  tags: Array<{ namespace: string, key: string }>
}

output = {
  tags: Array<{
    namespace: string,
    key: string,
    namespaceZh?: string,
    tagName?: string,
    intro?: string,
    found: boolean
  }>
}
```

行为：

- 使用 `byNamespaceAndKey` 精确查询。
- 未命中时保留原标签，`found=false`。

## agent 调用流程示例

```pseudo
agent receives: "帮我搜中文标签的漫画"

1. tagIndex.load({ cachePath: "./cache/ehtag-index.json" })
2. resolved = tagIndex.resolveKeyword({ keyword: "中文", autoSelect: true })
3. if resolved.source == "ambiguous":
     ask user to choose one candidate
4. gallery.search({ keyword: resolved.resolvedKeyword })
5. return galleries with translated tags
```

## 缓存与更新

缓存建议保存：

```pseudo
CacheFile {
  sourceSha: string
  timestamp: string
  version: number
  records: TagRecord[]
}
```

更新策略：

- 远程 `head.sha` 或 `head.committer.when` 变化时重建索引。
- 本地索引构建失败时保留旧缓存。
- 定期刷新即可，不需要每次查询都下载远程 JSON。

## 注意事项

- `name`、`intro`、`links` 是 HTML，展示前必须清理或明确作为 HTML 处理。
- 搜索时英文 key 建议统一小写，中文 tagName 保留原文但可建立小写副本。
- `rows` 命名空间主要描述命名空间本身，通常不作为普通漫画搜索标签优先返回。
- 简介匹配噪音较大，应低权重或仅在 `includeIntro=true` 时启用。
- 如果工具用于自动搜索漫画，默认只自动选择最高分且明显领先的候选；否则应返回候选列表让用户确认。
