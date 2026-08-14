package main

type copy struct {
	Language             string
	OGLocale             string
	Prefix               string
	LanguageLabel        string
	Title                string
	Description          string
	ImageAlt             string
	Skip                 string
	HomeAria             string
	PrimaryAria          string
	LanguageAria         string
	FooterAria           string
	NavHome              string
	NavStart             string
	NavProviders         string
	NavFeatures          string
	NavIntegrations      string
	NavSafety            string
	HeroEyebrow          string
	HeroLead             string
	HeroEmphasis         string
	HeroLede             string
	HeroStart            string
	HeroRead             string
	SnapshotEyebrow      string
	SnapshotTitle        string
	SnapshotCopy         string
	SourceLabel          string
	StableLabel          string
	RegistryLabel        string
	MatrixEyebrow        string
	MatrixTitle          string
	MatrixCopy           string
	ColumnChannel        string
	ColumnPackage        string
	ColumnSurface        string
	ColumnState          string
	ColumnVersion        string
	ColumnPath           string
	ColumnEvidence       string
	EvidenceLabel        string
	MeaningEyebrow       string
	MeaningTitle         string
	PublishedTitle       string
	PublishedCopy        string
	SourceTitle          string
	SourceCopy           string
	NotListedTitle       string
	NotListedCopy        string
	BoundaryEyebrow      string
	BoundaryTitle        string
	BoundaryCopy         string
	BoundaryDetail       string
	CTAeyebrow           string
	CTATitle             string
	CTACopy              string
	CTAStart             string
	CTAProviders         string
	FooterCopy           string
	FooterPrivacy        string
	FooterTerms          string
	FooterSource         string
	FooterSecurity       string
	FooterReleases       string
	NoVersion            string
	AutomaticReleasePath string
	StableOnlyPath       string
	SourcePath           string
	ManualPath           string
	ListSeparator        string
	Packages             map[string]string
	Surfaces             map[string]string
	States               map[string]string
}

func copies() []copy {
	return []copy{englishCopy(), japaneseCopy(), simplifiedChineseCopy(), traditionalChineseCopy(), koreanCopy()}
}

func sharedPackages(values ...string) map[string]string {
	keys := []string{"archives-and-mcpb", "mcpb", "plugin", "shared-plugin", "extension", "power", "config-only"}
	result := make(map[string]string, len(keys))
	for index, key := range keys {
		result[key] = values[index]
	}
	return result
}

func englishCopy() copy {
	return copy{
		Language: "en", OGLocale: "en_GB", LanguageLabel: "EN",
		Title:       "Corresync integrations — local MCP, plugins, and verified availability",
		Description: "See which Corresync MCP, plugin, extension, Power, and marketplace paths are published, source-available, or not yet listed for local AI agents.",
		ImageAlt:    "Corresync mark beside its local, provider-neutral MCP promise.",
		Skip:        "Skip to content", HomeAria: "Corresync home", PrimaryAria: "Primary", LanguageAria: "Language", FooterAria: "Footer",
		NavHome: "Home", NavStart: "Getting started", NavProviders: "Providers", NavFeatures: "Features", NavIntegrations: "Integrations", NavSafety: "Safety",
		HeroEyebrow: "Agent integrations, without inflated claims", HeroLead: "Use Corresync where your agent runs.", HeroEmphasis: "See exactly how it is packaged.",
		HeroLede:  "Corresync connects to local AI agents over stdio. This matrix separates direct MCP setup, Skills, native packages, source availability, and actual marketplace publication—so a compatible client is never mistaken for an official listing.",
		HeroStart: "Set up detected agents", HeroRead: "Read the matrix",
		SnapshotEyebrow: "Version snapshot", SnapshotTitle: "Three versions, three different facts.",
		SnapshotCopy: "The source bundle can be ahead of the latest stable release, while an external registry can still show an older verified record. They are displayed separately on purpose.",
		SourceLabel:  "Source bundle", StableLabel: "Latest stable release", RegistryLabel: "MCP Registry observed",
		MatrixEyebrow: "Publication matrix", MatrixTitle: "What is available now—and where.",
		MatrixCopy:    "Every working integration below is local. “Source available” means the reviewed package is in this repository; it does not mean the upstream vendor lists or endorses it.",
		ColumnChannel: "Channel", ColumnPackage: "Package", ColumnSurface: "Works in", ColumnState: "Directory state", ColumnVersion: "Version", ColumnPath: "How it ships", ColumnEvidence: "Evidence", EvidenceLabel: "Check",
		MeaningEyebrow: "Read the labels literally", MeaningTitle: "Compatibility, packaging, and publication are separate.",
		PublishedTitle: "Published", PublishedCopy: "An externally visible record was checked at the version shown. Stable release automation must verify it again after publishing.",
		SourceTitle: "Source available", SourceCopy: "A versioned native package is generated and tested in this repository. An official vendor directory has not been claimed.",
		NotListedTitle: "Not listed", NotListedCopy: "Use corr setup for the reviewed direct MCP path. Marketplace submission waits for native metadata and upstream review.",
		BoundaryEyebrow: "Local means local", BoundaryTitle: "No hosted agent or private tunnel is enabled by a listing.",
		BoundaryCopy:   "Corresync exposes one local stdio server and local account state. Hosted ChatGPT, cloud agents, Kiro Web, and remote sandboxes cannot reach it merely because a plugin format exists.",
		BoundaryDetail: "A hosted relay or private MCP tunnel would introduce a new data path and requires its own architecture decision, consent, privacy disclosure, revocation design, and security review.",
		CTAeyebrow:     "The shortest path", CTATitle: "Let corr setup detect and connect the agents on this machine.",
		CTACopy: "The matrix is reference material. The guided command remains the natural starting point and only offers reviewed local paths.", CTAStart: "Open getting started", CTAProviders: "Check provider routes",
		FooterCopy:    "Independent local-first mail, calendar, and task tooling. Not affiliated with or endorsed by any provider or agent vendor named here.",
		FooterPrivacy: "Privacy", FooterTerms: "Terms", FooterSource: "GitHub source", FooterSecurity: "Security", FooterReleases: "Releases",
		NoVersion: "Not claimed", AutomaticReleasePath: "Verified release automation", StableOnlyPath: "Stable tags only", SourcePath: "Versioned repository source", ManualPath: "Direct setup; directory pending", ListSeparator: ", ",
		Packages: sharedPackages("Release archives + MCPB", "Self-contained MCPB", "Thin plugin", "Shared plugin", "Thin extension", "Thin Power", "Direct MCP config"),
		Surfaces: map[string]string{"local-cli": "local CLI", "local-desktop": "local desktop", "local-ide": "local IDE"},
		States:   map[string]string{"published": "Published", "source-available": "Source available", "not-listed": "Not listed"},
	}
}

func japaneseCopy() copy {
	return copy{
		Language: "ja", OGLocale: "ja_JP", Prefix: "ja", LanguageLabel: "日本語",
		Title:       "CorresyncのAI連携 — ローカルMCP・プラグイン・公開状況",
		Description: "CorresyncをAIエージェントで使う方法を、MCP、Skill、プラグイン、拡張機能、マーケット掲載の違いまで含めて正確に確認できます。",
		ImageAlt:    "Corresyncのロゴと、ローカルで動くプロバイダー中立MCPという説明。",
		Skip:        "本文へ移動", HomeAria: "Corresync ホーム", PrimaryAria: "メインメニュー", LanguageAria: "言語", FooterAria: "フッター",
		NavHome: "ホーム", NavStart: "はじめる", NavProviders: "対応サービス", NavFeatures: "できること", NavIntegrations: "AI連携", NavSafety: "安全性",
		HeroEyebrow: "誇張しないAIエージェント連携", HeroLead: "いつものエージェントで使える。", HeroEmphasis: "導入方法も、公開状況も明確に。",
		HeroLede:  "Corresyncは、端末上のAIエージェントとstdioでつながります。この一覧では、MCPの直接設定、Skill、専用パッケージ、リポジトリでの提供、公式マーケット掲載を分けて表示します。「対応」と「公式掲載」を混同しません。",
		HeroStart: "検出したエージェントを設定", HeroRead: "対応表を見る",
		SnapshotEyebrow: "バージョンの見方", SnapshotTitle: "3つの数字は、それぞれ別の事実です。",
		SnapshotCopy: "開発中のソースは安定版より先に進み、外部レジストリには確認済みの旧版が残ることがあります。意図的に別々に表示しています。",
		SourceLabel:  "ソース内パッケージ", StableLabel: "最新の安定版", RegistryLabel: "MCP Registryで確認",
		MatrixEyebrow: "公開・配布状況", MatrixTitle: "いま、どこから何を使えるか。",
		MatrixCopy:    "実際に使える連携はすべてローカルです。「ソースで提供」は、このリポジトリに検証対象のパッケージがあるという意味で、各社の公式マーケット掲載や推奨を意味しません。",
		ColumnChannel: "提供先", ColumnPackage: "形式", ColumnSurface: "使える場所", ColumnState: "公開状況", ColumnVersion: "バージョン", ColumnPath: "提供方法", ColumnEvidence: "確認", EvidenceLabel: "確認する",
		MeaningEyebrow: "表示の意味", MeaningTitle: "対応・パッケージ・公開は別々です。",
		PublishedTitle: "公開済み", PublishedCopy: "表示した版の外部レコードを確認済みです。安定版の公開処理でも、公開後に同じ内容を再検証します。",
		SourceTitle: "ソースで提供", SourceCopy: "版付きの専用パッケージをリポジトリで生成・テストしています。各社の公式ディレクトリ掲載は主張しません。",
		NotListedTitle: "未掲載", NotListedCopy: "現時点ではcorr setupによる検証済みのMCP直接設定を使えます。専用メタデータと審査が整ってから申請します。",
		BoundaryEyebrow: "ローカルは、端末の中", BoundaryTitle: "掲載されても、ホスト型エージェントや非公開トンネルは有効になりません。",
		BoundaryCopy:   "Corresyncが公開するのは、ローカルのstdioサーバーとローカルのアカウント状態だけです。プラグイン形式があっても、ChatGPTのホスト環境、クラウドエージェント、Kiro Web、リモートサンドボックスからは到達できません。",
		BoundaryDetail: "リレーや非公開MCPトンネルは新しいデータ経路になります。導入には、別の設計判断、明示的な同意、プライバシー開示、失効方法、セキュリティ審査が必要です。",
		CTAeyebrow:     "いちばん短い始め方", CTATitle: "この端末のエージェントはcorr setupに検出・接続を任せられます。",
		CTACopy: "対応表は確認用です。最初はガイドに沿って進めれば、検証済みのローカル経路だけが提示されます。", CTAStart: "スタートガイドを開く", CTAProviders: "プロバイダー経路を確認",
		FooterCopy:    "独立したローカルファーストのメール・カレンダー・タスクツールです。記載した企業やサービスの公式製品ではありません。",
		FooterPrivacy: "プライバシー", FooterTerms: "利用規約", FooterSource: "ソースコード", FooterSecurity: "脆弱性の報告", FooterReleases: "リリース",
		NoVersion: "表明なし", AutomaticReleasePath: "検証済みリリースから自動", StableOnlyPath: "安定版タグのみ", SourcePath: "版付きリポジトリソース", ManualPath: "直接設定可・掲載は準備中", ListSeparator: "・",
		Packages: sharedPackages("リリース一式＋MCPB", "自己完結MCPB", "薄いプラグイン", "共通プラグイン", "薄い拡張機能", "薄いPower", "MCP直接設定"),
		Surfaces: map[string]string{"local-cli": "ローカルCLI", "local-desktop": "ローカルデスクトップ", "local-ide": "ローカルIDE"},
		States:   map[string]string{"published": "公開済み", "source-available": "ソースで提供", "not-listed": "未掲載"},
	}
}

func simplifiedChineseCopy() copy {
	return copy{
		Language: "zh-Hans", OGLocale: "zh_CN", Prefix: "zh-cn", LanguageLabel: "简中",
		Title:       "Corresync AI 集成 — 本地 MCP、插件与真实发布状态",
		Description: "准确查看 Corresync 在各类 AI 代理中的 MCP、Skill、插件、扩展、Power 与市场上架状态。",
		ImageAlt:    "Corresync 标志及其本地、供应商中立的 MCP 定位。",
		Skip:        "跳到正文", HomeAria: "Corresync 首页", PrimaryAria: "主导航", LanguageAria: "语言", FooterAria: "页脚",
		NavHome: "首页", NavStart: "开始使用", NavProviders: "服务商", NavFeatures: "功能", NavIntegrations: "AI 集成", NavSafety: "安全",
		HeroEyebrow: "不夸大能力的 AI 集成", HeroLead: "在常用代理中使用 Corresync。", HeroEmphasis: "封装方式与发布状态一目了然。",
		HeroLede:  "Corresync 通过 stdio 连接本机 AI 代理。本表分别标明直接 MCP 配置、Skill、原生封装、仓库源码和官方市场上架情况，不会把“兼容”说成“已官方上架”。",
		HeroStart: "配置检测到的代理", HeroRead: "查看完整矩阵",
		SnapshotEyebrow: "版本快照", SnapshotTitle: "三个版本号，代表三个不同事实。",
		SnapshotCopy: "开发中的源码可能领先于最新稳定版，外部注册表也可能仍显示较早的已验证记录，因此这里刻意分开呈现。",
		SourceLabel:  "源码封装", StableLabel: "最新稳定版", RegistryLabel: "MCP Registry 实测",
		MatrixEyebrow: "发布矩阵", MatrixTitle: "现在能在哪里获得什么。",
		MatrixCopy:    "所有可用集成都只在本机运行。“源码可用”仅表示本仓库含有经过审查的封装，并不代表上游厂商已上架或背书。",
		ColumnChannel: "渠道", ColumnPackage: "形式", ColumnSurface: "适用位置", ColumnState: "目录状态", ColumnVersion: "版本", ColumnPath: "提供方式", ColumnEvidence: "依据", EvidenceLabel: "核验",
		MeaningEyebrow: "按字面理解状态", MeaningTitle: "兼容、封装和发布互不等同。",
		PublishedTitle: "已发布", PublishedCopy: "已核验所示版本的外部公开记录。稳定版自动化在发布后还会再次核对。",
		SourceTitle: "源码可用", SourceCopy: "仓库会生成并测试带版本的原生封装，但不声称已进入厂商官方目录。",
		NotListedTitle: "尚未上架", NotListedCopy: "目前可用 corr setup 完成经过审查的直接 MCP 配置；原生元数据和上游审核就绪后才会提交。",
		BoundaryEyebrow: "本地就是本地", BoundaryTitle: "目录上架不会自动启用云端代理或私有隧道。",
		BoundaryCopy:   "Corresync 只提供本地 stdio 服务和本地账户状态。即使存在插件格式，托管版 ChatGPT、云端代理、Kiro Web 和远程沙箱也无法直接访问。",
		BoundaryDetail: "托管中继或私有 MCP 隧道会形成新的数据路径，必须另行完成架构决策、明确同意、隐私披露、撤销设计和安全审查。",
		CTAeyebrow:     "最快的开始方式", CTATitle: "让 corr setup 检测并连接这台设备上的代理。",
		CTACopy: "矩阵用于查阅；引导命令仍是自然入口，并且只会提供经过审查的本地路径。", CTAStart: "打开入门指南", CTAProviders: "检查服务商路径",
		FooterCopy:    "独立的本地优先邮件、日历和任务工具。与此处提到的服务商或 AI 代理厂商没有隶属或背书关系。",
		FooterPrivacy: "隐私", FooterTerms: "使用条款", FooterSource: "GitHub 源码", FooterSecurity: "安全报告", FooterReleases: "版本发布",
		NoVersion: "未声明", AutomaticReleasePath: "验证发布后自动提供", StableOnlyPath: "仅稳定版标签", SourcePath: "带版本的仓库源码", ManualPath: "可直接配置，目录待上架", ListSeparator: "、",
		Packages: sharedPackages("发布压缩包 + MCPB", "自包含 MCPB", "轻量插件", "共享插件", "轻量扩展", "轻量 Power", "直接 MCP 配置"),
		Surfaces: map[string]string{"local-cli": "本地 CLI", "local-desktop": "本地桌面端", "local-ide": "本地 IDE"},
		States:   map[string]string{"published": "已发布", "source-available": "源码可用", "not-listed": "尚未上架"},
	}
}

func traditionalChineseCopy() copy {
	return copy{
		Language: "zh-Hant", OGLocale: "zh_TW", Prefix: "zh-tw", LanguageLabel: "繁中",
		Title:       "Corresync AI 整合 — 本機 MCP、外掛與實際發布狀態",
		Description: "清楚查看 Corresync 在各種 AI 代理中的 MCP、Skill、外掛、擴充套件、Power 與市集上架狀態。",
		ImageAlt:    "Corresync 標誌與本機、供應商中立 MCP 的定位說明。",
		Skip:        "跳到主要內容", HomeAria: "Corresync 首頁", PrimaryAria: "主要導覽", LanguageAria: "語言", FooterAria: "頁尾",
		NavHome: "首頁", NavStart: "開始使用", NavProviders: "服務商", NavFeatures: "功能", NavIntegrations: "AI 整合", NavSafety: "安全",
		HeroEyebrow: "不誇大能力的 AI 整合", HeroLead: "在慣用的代理中使用 Corresync。", HeroEmphasis: "封裝方式與發布狀態都說清楚。",
		HeroLede:  "Corresync 透過 stdio 連接本機 AI 代理。本表分開標示直接 MCP 設定、Skill、原生封裝、儲存庫原始碼與官方市集上架情況，不會把「相容」說成「官方已上架」。",
		HeroStart: "設定偵測到的代理", HeroRead: "查看完整矩陣",
		SnapshotEyebrow: "版本快照", SnapshotTitle: "三個版本號，代表三件不同的事。",
		SnapshotCopy: "開發中的原始碼可能領先最新穩定版，外部登錄檔也可能仍顯示較舊的已驗證紀錄，因此這裡刻意分開呈現。",
		SourceLabel:  "原始碼封裝", StableLabel: "最新穩定版", RegistryLabel: "MCP Registry 實際查得",
		MatrixEyebrow: "發布矩陣", MatrixTitle: "目前能從哪裡取得什麼。",
		MatrixCopy:    "所有可用整合都只在本機執行。「原始碼可用」只代表本儲存庫含有經過審查的封裝，不代表上游廠商已上架或背書。",
		ColumnChannel: "管道", ColumnPackage: "形式", ColumnSurface: "適用位置", ColumnState: "目錄狀態", ColumnVersion: "版本", ColumnPath: "提供方式", ColumnEvidence: "依據", EvidenceLabel: "核對",
		MeaningEyebrow: "按字面理解狀態", MeaningTitle: "相容、封裝與發布互不等同。",
		PublishedTitle: "已發布", PublishedCopy: "已核對所示版本的外部公開紀錄。穩定版自動流程在發布後還會再次驗證。",
		SourceTitle: "原始碼可用", SourceCopy: "儲存庫會產生並測試含版本的原生封裝，但不宣稱已進入廠商官方目錄。",
		NotListedTitle: "尚未上架", NotListedCopy: "目前可用 corr setup 完成經過審查的直接 MCP 設定；原生中繼資料與上游審查就緒後才會提交。",
		BoundaryEyebrow: "本機就是本機", BoundaryTitle: "目錄上架不會自動啟用雲端代理或私人通道。",
		BoundaryCopy:   "Corresync 只提供本機 stdio 服務與本機帳戶狀態。即使已有外掛格式，託管版 ChatGPT、雲端代理、Kiro Web 與遠端沙箱仍無法直接存取。",
		BoundaryDetail: "託管中繼或私人 MCP 通道會形成新的資料路徑，必須另行完成架構決策、明確同意、隱私揭露、撤銷設計與安全審查。",
		CTAeyebrow:     "最快的開始方式", CTATitle: "讓 corr setup 偵測並連接這台裝置上的代理。",
		CTACopy: "矩陣供查閱；引導指令仍是自然入口，而且只會提供經過審查的本機路徑。", CTAStart: "開啟入門指南", CTAProviders: "檢查服務商路徑",
		FooterCopy:    "獨立的本機優先郵件、日曆與工作工具。與此處提到的服務商或 AI 代理廠商沒有隸屬或背書關係。",
		FooterPrivacy: "隱私權", FooterTerms: "使用條款", FooterSource: "GitHub 原始碼", FooterSecurity: "安全性回報", FooterReleases: "版本發布",
		NoVersion: "未聲明", AutomaticReleasePath: "驗證發布後自動提供", StableOnlyPath: "僅穩定版標籤", SourcePath: "含版本的儲存庫原始碼", ManualPath: "可直接設定，目錄待上架", ListSeparator: "、",
		Packages: sharedPackages("發布壓縮檔 + MCPB", "自包含 MCPB", "輕量外掛", "共用外掛", "輕量擴充套件", "輕量 Power", "直接 MCP 設定"),
		Surfaces: map[string]string{"local-cli": "本機 CLI", "local-desktop": "本機桌面端", "local-ide": "本機 IDE"},
		States:   map[string]string{"published": "已發布", "source-available": "原始碼可用", "not-listed": "尚未上架"},
	}
}

func koreanCopy() copy {
	return copy{
		Language: "ko", OGLocale: "ko_KR", Prefix: "ko", LanguageLabel: "한국어",
		Title:       "Corresync AI 연동 — 로컬 MCP, 플러그인, 실제 배포 상태",
		Description: "Corresync의 MCP, Skill, 플러그인, 확장, Power와 마켓 등록 상태를 로컬 AI 에이전트별로 정확히 확인하세요.",
		ImageAlt:    "Corresync 로고와 로컬 공급자 중립 MCP라는 설명.",
		Skip:        "본문으로 이동", HomeAria: "Corresync 홈", PrimaryAria: "주 메뉴", LanguageAria: "언어", FooterAria: "바닥글",
		NavHome: "홈", NavStart: "시작하기", NavProviders: "서비스", NavFeatures: "기능", NavIntegrations: "AI 연동", NavSafety: "안전",
		HeroEyebrow: "과장 없는 AI 에이전트 연동", HeroLead: "평소 쓰는 에이전트에서 Corresync를 사용하세요.", HeroEmphasis: "패키지와 배포 상태도 정확하게.",
		HeroLede:  "Corresync는 stdio로 로컬 AI 에이전트에 연결됩니다. 직접 MCP 설정, Skill, 네이티브 패키지, 저장소 제공 여부, 공식 마켓 등록을 따로 표시해 ‘호환됨’을 ‘공식 등록됨’으로 오해하지 않게 합니다.",
		HeroStart: "감지된 에이전트 설정", HeroRead: "전체 표 보기",
		SnapshotEyebrow: "버전 현황", SnapshotTitle: "세 버전은 서로 다른 사실을 뜻합니다.",
		SnapshotCopy: "개발 중인 소스는 최신 안정판보다 앞설 수 있고, 외부 레지스트리에는 더 오래된 검증 기록이 남을 수 있어 의도적으로 구분해 표시합니다.",
		SourceLabel:  "소스 패키지", StableLabel: "최신 안정판", RegistryLabel: "MCP Registry 확인값",
		MatrixEyebrow: "배포 현황", MatrixTitle: "지금 무엇을 어디에서 쓸 수 있는지 확인하세요.",
		MatrixCopy:    "사용 가능한 연동은 모두 로컬에서만 동작합니다. ‘소스 제공’은 이 저장소에 검토된 패키지가 있다는 뜻이며, 공급자의 공식 마켓 등록이나 보증을 뜻하지 않습니다.",
		ColumnChannel: "채널", ColumnPackage: "형식", ColumnSurface: "사용 위치", ColumnState: "등록 상태", ColumnVersion: "버전", ColumnPath: "제공 방식", ColumnEvidence: "근거", EvidenceLabel: "확인",
		MeaningEyebrow: "상태를 그대로 읽기", MeaningTitle: "호환성, 패키지, 공개 여부는 각각 다릅니다.",
		PublishedTitle: "공개됨", PublishedCopy: "표시된 버전의 외부 공개 기록을 확인했습니다. 안정판 자동화도 게시 후 같은 내용을 다시 검증합니다.",
		SourceTitle: "소스 제공", SourceCopy: "버전이 붙은 네이티브 패키지를 저장소에서 생성하고 테스트하지만 공급자 공식 디렉터리 등록을 주장하지 않습니다.",
		NotListedTitle: "미등록", NotListedCopy: "현재는 corr setup으로 검토된 직접 MCP 설정을 사용할 수 있습니다. 네이티브 메타데이터와 상위 검토가 준비된 뒤 제출합니다.",
		BoundaryEyebrow: "로컬은 로컬 그대로", BoundaryTitle: "목록에 올라가도 호스팅 에이전트나 비공개 터널이 켜지지 않습니다.",
		BoundaryCopy:   "Corresync는 로컬 stdio 서버와 로컬 계정 상태만 제공합니다. 플러그인 형식이 있어도 호스팅 ChatGPT, 클라우드 에이전트, Kiro Web, 원격 샌드박스에서는 접근할 수 없습니다.",
		BoundaryDetail: "호스팅 릴레이나 비공개 MCP 터널은 새로운 데이터 경로입니다. 별도의 아키텍처 결정, 명시적 동의, 개인정보 고지, 철회 설계, 보안 검토가 필요합니다.",
		CTAeyebrow:     "가장 빠른 시작", CTATitle: "corr setup으로 이 기기의 에이전트를 감지하고 연결하세요.",
		CTACopy: "이 표는 참고 자료입니다. 안내 명령이 기본 시작점이며 검토된 로컬 경로만 제안합니다.", CTAStart: "시작 안내 열기", CTAProviders: "서비스 경로 확인",
		FooterCopy:    "독립적인 로컬 우선 메일·캘린더·작업 도구입니다. 여기에 언급된 서비스 또는 AI 에이전트 공급자와 제휴하거나 보증받지 않았습니다.",
		FooterPrivacy: "개인정보", FooterTerms: "이용약관", FooterSource: "GitHub 소스", FooterSecurity: "보안 제보", FooterReleases: "릴리스",
		NoVersion: "표시 안 함", AutomaticReleasePath: "검증된 릴리스에서 자동 제공", StableOnlyPath: "안정판 태그만", SourcePath: "버전이 붙은 저장소 소스", ManualPath: "직접 설정 가능·목록 미등록", ListSeparator: " · ",
		Packages: sharedPackages("릴리스 압축 파일 + MCPB", "자체 포함 MCPB", "경량 플러그인", "공유 플러그인", "경량 확장", "경량 Power", "직접 MCP 설정"),
		Surfaces: map[string]string{"local-cli": "로컬 CLI", "local-desktop": "로컬 데스크톱", "local-ide": "로컬 IDE"},
		States:   map[string]string{"published": "공개됨", "source-available": "소스 제공", "not-listed": "미등록"},
	}
}
