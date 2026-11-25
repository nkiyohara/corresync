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
		Title:       "Corresync agent integrations — MCP setup, plugins, and where they are published",
		Description: "Which Corresync MCP, plugin, extension, Power, and marketplace packages for local AI agents are published, available in the repository, or not yet listed.",
		ImageAlt:    "Corresync mark beside the words: one local MCP for mail, calendars, and tasks — provider-neutral, preview before commit.",
		Skip:        "Skip to content", HomeAria: "Corresync home", PrimaryAria: "Primary", LanguageAria: "Language", FooterAria: "Footer",
		NavHome: "Home", NavStart: "Getting started", NavProviders: "Providers", NavFeatures: "Features", NavIntegrations: "Integrations", NavSafety: "Safety",
		HeroEyebrow: "Agent integrations", HeroLead: "How Corresync is packaged", HeroEmphasis: "for each agent",
		HeroLede:  "Corresync connects to local AI agents over stdio. The table below lists direct MCP setup, Skills, native packages, what is in the repository, and what is actually published in a marketplace. A client that works with Corresync does not mean there is an official listing.",
		HeroStart: "Set up detected agents", HeroRead: "See the table",
		SnapshotEyebrow: "Versions", SnapshotTitle: "Current versions",
		SnapshotCopy: "The source bundle can be newer than the latest stable release, and an external registry can still show an older verified record, so the three are listed separately.",
		SourceLabel:  "Source bundle", StableLabel: "Latest stable release", RegistryLabel: "MCP Registry observed",
		MatrixEyebrow: "Publication table", MatrixTitle: "What is available and where",
		MatrixCopy:    "Every integration below runs locally. “Source available” means the reviewed package is in this repository. It does not mean the vendor lists or endorses it.",
		ColumnChannel: "Channel", ColumnPackage: "Package", ColumnSurface: "Works in", ColumnState: "Directory state", ColumnVersion: "Version", ColumnPath: "How it ships", ColumnEvidence: "Evidence", EvidenceLabel: "Check",
		MeaningEyebrow: "Labels", MeaningTitle: "What the labels mean",
		PublishedTitle: "Published", PublishedCopy: "A public record was checked at the version shown. The stable release automation must check it again after publishing.",
		SourceTitle: "Source available", SourceCopy: "A versioned native package is built and tested in this repository. We do not claim a listing in the vendor’s official directory.",
		NotListedTitle: "Not listed", NotListedCopy: "Use corr setup to add Corresync directly as an MCP server. Marketplace submission waits for native metadata and the vendor’s review.",
		BoundaryEyebrow: "Local only", BoundaryTitle: "A listing does not enable hosted agents or tunnels",
		BoundaryCopy:   "Corresync runs one local stdio server and keeps account state on your computer. Hosted ChatGPT, cloud agents, Kiro Web, and remote sandboxes cannot reach it just because a plugin format exists for them.",
		BoundaryDetail: "A hosted relay or private MCP tunnel would add a new path for your data. It would need its own architecture decision, consent, privacy disclosure, revocation design, and security review.",
		CTAeyebrow:     "Setup", CTATitle: "corr setup finds and connects the agents on this computer",
		CTACopy: "The table is for reference. Start with the guided command; it only offers reviewed local setups.", CTAStart: "Open getting started", CTAProviders: "See provider routes",
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
		Description: "CorresyncをAIエージェントから使う方法と、MCPの直接設定、Skill、プラグイン、拡張機能ごとの提供状況やマーケットへの掲載状況をまとめています。",
		ImageAlt:    "Corresyncのロゴと、ローカルで動くMCPサーバーであることを示す説明。",
		Skip:        "本文へ移動", HomeAria: "Corresync ホーム", PrimaryAria: "メインメニュー", LanguageAria: "言語", FooterAria: "フッター",
		NavHome: "ホーム", NavStart: "はじめる", NavProviders: "対応サービス", NavFeatures: "できること", NavIntegrations: "AI連携", NavSafety: "安全性",
		HeroEyebrow: "AI連携", HeroLead: "AIエージェントとの連携", HeroEmphasis: "導入方法と公開状況",
		HeroLede:  "Corresyncは、このコンピューター上のAIエージェントとstdioで接続します。この表では、MCPの直接設定、Skill、専用パッケージ、リポジトリでの提供、公式マーケットへの掲載を分けて表示しています。対応していることと、公式に掲載されていることは別です。",
		HeroStart: "検出したエージェントを設定", HeroRead: "対応表",
		SnapshotEyebrow: "バージョン", SnapshotTitle: "3つのバージョン番号の意味",
		SnapshotCopy: "開発中のソースは最新の安定版より新しいことがあり、外部のレジストリには確認済みの古い版が表示されたままのことがあります。そのため3つを分けて表示しています。",
		SourceLabel:  "ソース内のパッケージ", StableLabel: "最新の安定版", RegistryLabel: "MCP Registryで確認した版",
		MatrixEyebrow: "公開・配布状況", MatrixTitle: "提供先ごとの対応状況",
		MatrixCopy:    "使える連携はすべてローカルで動きます。「ソースで提供」は、このリポジトリに検証済みのパッケージがあるという意味です。各社の公式マーケットへの掲載や推奨を意味するものではありません。",
		ColumnChannel: "提供先", ColumnPackage: "形式", ColumnSurface: "使える場所", ColumnState: "公開状況", ColumnVersion: "バージョン", ColumnPath: "提供方法", ColumnEvidence: "確認", EvidenceLabel: "確認する",
		MeaningEyebrow: "表示の意味", MeaningTitle: "対応、パッケージ、公開はそれぞれ別",
		PublishedTitle: "公開済み", PublishedCopy: "表示しているバージョンの外部の公開情報を確認済みです。安定版のリリース処理でも、公開後に同じ確認をもう一度行います。",
		SourceTitle: "ソースで提供", SourceCopy: "バージョン付きの専用パッケージを、このリポジトリで生成してテストしています。各社の公式ディレクトリに掲載されているとはしていません。",
		NotListedTitle: "未掲載", NotListedCopy: "今はcorr setupで、検証済みのMCP直接設定を使えます。専用のメタデータと先方の審査の準備ができてから申請します。",
		BoundaryEyebrow: "ローカルでの動作", BoundaryTitle: "掲載されても、ホスト型のエージェントや非公開トンネルから使えるようにはなりません",
		BoundaryCopy:   "Corresyncが提供するのは、ローカルのstdioサーバーとローカルのアカウント状態だけです。プラグインの形式があっても、ChatGPTのホスト環境、クラウドのエージェント、Kiro Web、リモートのサンドボックスからは接続できません。",
		BoundaryDetail: "ホスト型のリレーや非公開のMCPトンネルを使うと、新しいデータの通り道ができます。導入するには、別途の設計判断、利用者の同意、プライバシーに関する開示、取り消しの仕組み、セキュリティ審査が必要です。",
		CTAeyebrow:     "はじめ方", CTATitle: "corr setupで、このコンピューターのエージェントを検出して接続できます",
		CTACopy: "対応表は参照用です。まずはガイド付き設定から始めてください。検証済みのローカルでの接続方法だけが表示されます。", CTAStart: "スタートガイド", CTAProviders: "プロバイダーの接続方法",
		FooterCopy:    "独立したローカルファーストのメール・カレンダー・タスクツールです。記載した企業やサービスの公式製品ではありません。",
		FooterPrivacy: "プライバシー", FooterTerms: "利用規約", FooterSource: "ソースコード", FooterSecurity: "脆弱性の報告", FooterReleases: "リリース",
		NoVersion: "表明なし", AutomaticReleasePath: "検証済みリリースから自動", StableOnlyPath: "安定版タグのみ", SourcePath: "バージョン付きのリポジトリソース", ManualPath: "直接設定可・掲載は準備中", ListSeparator: "・",
		Packages: sharedPackages("リリース一式＋MCPB", "自己完結型MCPB", "軽量プラグイン", "共通プラグイン", "軽量な拡張機能", "軽量なPower", "MCP直接設定"),
		Surfaces: map[string]string{"local-cli": "ローカルCLI", "local-desktop": "ローカルデスクトップ", "local-ide": "ローカルIDE"},
		States:   map[string]string{"published": "公開済み", "source-available": "ソースで提供", "not-listed": "未掲載"},
	}
}

func simplifiedChineseCopy() copy {
	return copy{
		Language: "zh-Hans", OGLocale: "zh_CN", Prefix: "zh-cn", LanguageLabel: "简中",
		Title:       "Corresync AI 集成：本地 MCP、插件和发布状态",
		Description: "查看 Corresync 的 MCP、Skill、插件、扩展、Power 在各个本地 AI 智能体中是已发布、仅提供源码，还是尚未上架。",
		ImageAlt:    "Corresync 标志和本地 MCP 服务器说明。",
		Skip:        "跳到正文", HomeAria: "Corresync 首页", PrimaryAria: "主导航", LanguageAria: "语言", FooterAria: "页脚",
		NavHome: "首页", NavStart: "快速开始", NavProviders: "支持服务", NavFeatures: "功能", NavIntegrations: "AI 集成", NavSafety: "安全",
		HeroEyebrow: "AI 智能体集成", HeroLead: "在常用的智能体中使用 Corresync。", HeroEmphasis: "封装方式和发布状态见下表。",
		HeroLede:  "Corresync 通过 stdio 连接本机的 AI 智能体。下表分别列出直接 MCP 配置、Skill、原生封装、仓库源码和官方市场上架情况。客户端兼容不等于已在官方市场上架。",
		HeroStart: "配置检测到的智能体", HeroRead: "查看表格",
		SnapshotEyebrow: "版本", SnapshotTitle: "三个版本号",
		SnapshotCopy: "仓库源码可能比最新稳定版更新，外部注册表也可能还显示之前核验过的旧版本，所以三者分开显示。",
		SourceLabel:  "源码封装", StableLabel: "最新稳定版", RegistryLabel: "MCP Registry 上的版本",
		MatrixEyebrow: "发布情况", MatrixTitle: "各渠道的提供情况",
		MatrixCopy:    "下列集成都在本机运行。“源码可用”只表示本仓库中有经过审查的封装，不代表上游厂商已上架或认可。",
		ColumnChannel: "渠道", ColumnPackage: "形式", ColumnSurface: "适用位置", ColumnState: "目录状态", ColumnVersion: "版本", ColumnPath: "提供方式", ColumnEvidence: "依据", EvidenceLabel: "核验",
		MeaningEyebrow: "状态说明", MeaningTitle: "兼容、封装和发布是三回事",
		PublishedTitle: "已发布", PublishedCopy: "已核验所示版本的外部公开记录。稳定版发布流程会在发布后再核验一次。",
		SourceTitle: "源码可用", SourceCopy: "仓库会生成并测试带版本号的原生封装，但不声称已进入厂商的官方目录。",
		NotListedTitle: "尚未上架", NotListedCopy: "可以用 corr setup 完成经过审查的直接 MCP 配置。原生元数据和上游审核就绪后才会提交上架。",
		BoundaryEyebrow: "只在本地运行", BoundaryTitle: "上架目录不会启用云端智能体或私有隧道",
		BoundaryCopy:   "Corresync 只提供本地 stdio 服务和本地账号状态。即使有对应的插件格式，托管版 ChatGPT、云端智能体、Kiro Web 和远程沙箱也无法访问它。",
		BoundaryDetail: "托管中继或私有 MCP 隧道会带来新的数据路径，需要单独的架构决策、同意、隐私说明、撤销机制和安全审查。",
		CTAeyebrow:     "开始使用", CTATitle: "运行 corr setup，检测并连接本机上的智能体",
		CTACopy: "此表仅供查阅。初次配置请运行 corr setup，它只会提供经过审查的本地连接方式。", CTAStart: "打开快速开始", CTAProviders: "查看各服务商的连接方式",
		FooterCopy:    "独立开发的本地邮件、日历和任务工具，与本页提到的服务商或 AI 智能体厂商没有隶属或背书关系。",
		FooterPrivacy: "隐私", FooterTerms: "条款", FooterSource: "源代码", FooterSecurity: "安全报告", FooterReleases: "发布",
		NoVersion: "未声明", AutomaticReleasePath: "验证发布后自动提供", StableOnlyPath: "仅稳定版标签", SourcePath: "带版本的仓库源码", ManualPath: "可直接配置，目录待上架", ListSeparator: "、",
		Packages: sharedPackages("发布压缩包 + MCPB", "自包含 MCPB", "轻量插件", "共享插件", "轻量扩展", "轻量 Power", "直接 MCP 配置"),
		Surfaces: map[string]string{"local-cli": "本地 CLI", "local-desktop": "本地桌面端", "local-ide": "本地 IDE"},
		States:   map[string]string{"published": "已发布", "source-available": "源码可用", "not-listed": "尚未上架"},
	}
}

func traditionalChineseCopy() copy {
	return copy{
		Language: "zh-Hant", OGLocale: "zh_TW", Prefix: "zh-tw", LanguageLabel: "繁中",
		Title:       "Corresync AI 整合：本機 MCP、外掛與發布狀態",
		Description: "查看 Corresync 的 MCP、Skill、外掛、擴充套件、Power 在各個本機 AI 代理中是已發布、只提供原始碼，還是尚未上架。",
		ImageAlt:    "Corresync 標誌與本機 MCP 伺服器說明。",
		Skip:        "跳到主要內容", HomeAria: "Corresync 首頁", PrimaryAria: "主要導覽", LanguageAria: "語言", FooterAria: "頁尾",
		NavHome: "首頁", NavStart: "開始使用", NavProviders: "支援服務", NavFeatures: "功能", NavIntegrations: "AI 整合", NavSafety: "安全",
		HeroEyebrow: "AI 代理整合", HeroLead: "在常用的 AI 代理中使用 Corresync。", HeroEmphasis: "封裝方式和發布狀態請見下表。",
		HeroLede:  "Corresync 透過 stdio 連接本機的 AI 代理。下表分別列出直接 MCP 設定、Skill、原生封裝、儲存庫原始碼和官方市集上架情況。用戶端相容不代表已在官方市集上架。",
		HeroStart: "設定偵測到的代理", HeroRead: "查看表格",
		SnapshotEyebrow: "版本", SnapshotTitle: "三個版本號",
		SnapshotCopy: "儲存庫原始碼可能比最新穩定版還新，外部登錄檔也可能仍顯示先前核對過的舊版本，所以三者分開顯示。",
		SourceLabel:  "原始碼封裝", StableLabel: "最新穩定版", RegistryLabel: "MCP Registry 上的版本",
		MatrixEyebrow: "發布情況", MatrixTitle: "各管道的提供情況",
		MatrixCopy:    "下列整合都在本機執行。「原始碼可用」只代表本儲存庫有經過審查的封裝，不代表上游廠商已上架或背書。",
		ColumnChannel: "管道", ColumnPackage: "形式", ColumnSurface: "適用位置", ColumnState: "目錄狀態", ColumnVersion: "版本", ColumnPath: "提供方式", ColumnEvidence: "依據", EvidenceLabel: "核對",
		MeaningEyebrow: "狀態說明", MeaningTitle: "相容、封裝和發布是不同的狀態",
		PublishedTitle: "已發布", PublishedCopy: "已核對所示版本的外部公開紀錄。穩定版發布流程會在發布後再核對一次。",
		SourceTitle: "原始碼可用", SourceCopy: "儲存庫會產生並測試含版本號的原生封裝，但不宣稱已列入廠商的官方目錄。",
		NotListedTitle: "尚未上架", NotListedCopy: "可以用 corr setup 完成經過審查的直接 MCP 設定。原生中繼資料和上游審查就緒後才會送出上架申請。",
		BoundaryEyebrow: "只在本機執行", BoundaryTitle: "上架目錄不會啟用雲端代理或私人通道",
		BoundaryCopy:   "Corresync 只提供本機 stdio 服務和本機帳號狀態。即使有對應的外掛格式，託管版 ChatGPT、雲端代理、Kiro Web 和遠端沙箱也無法存取它。",
		BoundaryDetail: "託管中繼或私人 MCP 通道會產生新的資料路徑，需要另外的架構決策、同意、隱私揭露、撤銷機制和安全審查。",
		CTAeyebrow:     "開始使用", CTATitle: "執行 corr setup，偵測並連接這台電腦上的 AI 代理",
		CTACopy: "此表僅供查閱。第一次設定請執行 corr setup，它只會提供經過審查的本機連線方式。", CTAStart: "開啟入門說明", CTAProviders: "查看各服務的連線方式",
		FooterCopy:    "獨立開發的本機郵件、行事曆與待辦工具，與這裡列出的服務商或 AI 代理廠商沒有隸屬或背書關係。",
		FooterPrivacy: "隱私", FooterTerms: "條款", FooterSource: "原始碼", FooterSecurity: "安全報告", FooterReleases: "發布",
		NoVersion: "未聲明", AutomaticReleasePath: "驗證發布後自動提供", StableOnlyPath: "僅穩定版標籤", SourcePath: "含版本的儲存庫原始碼", ManualPath: "可直接設定，目錄待上架", ListSeparator: "、",
		Packages: sharedPackages("發布壓縮檔 + MCPB", "自包含 MCPB", "輕量外掛", "共用外掛", "輕量擴充套件", "輕量 Power", "直接 MCP 設定"),
		Surfaces: map[string]string{"local-cli": "本機 CLI", "local-desktop": "本機桌面端", "local-ide": "本機 IDE"},
		States:   map[string]string{"published": "已發布", "source-available": "原始碼可用", "not-listed": "尚未上架"},
	}
}

func koreanCopy() copy {
	return copy{
		Language: "ko", OGLocale: "ko_KR", Prefix: "ko", LanguageLabel: "한국어",
		Title:       "Corresync AI 연동 — 로컬 MCP, 플러그인, 배포 상태",
		Description: "로컬 AI 에이전트용 Corresync MCP, Skill, 플러그인, 확장, Power의 공개, 소스 제공, 미등록 상태를 확인할 수 있습니다.",
		ImageAlt:    "Corresync 로고와 로컬 MCP 서버 설명.",
		Skip:        "본문으로 이동", HomeAria: "Corresync 홈", PrimaryAria: "주 메뉴", LanguageAria: "언어", FooterAria: "바닥글",
		NavHome: "홈", NavStart: "시작하기", NavProviders: "지원 서비스", NavFeatures: "기능", NavIntegrations: "AI 연동", NavSafety: "보안",
		HeroEyebrow: "AI 에이전트 연동", HeroLead: "평소 쓰는 에이전트에서 Corresync를 사용할 수 있습니다.", HeroEmphasis: "패키지 형식과 배포 상태를 확인하세요.",
		HeroLede:  "Corresync는 stdio로 로컬 AI 에이전트와 연결됩니다. 아래 표는 MCP 직접 설정, Skill, 네이티브 패키지, 저장소 제공, 공식 마켓 등록을 따로 표시합니다. 호환된다고 해서 공식 마켓에 등록된 것은 아닙니다.",
		HeroStart: "감지된 에이전트 설정", HeroRead: "표 보기",
		SnapshotEyebrow: "버전", SnapshotTitle: "세 가지 버전 번호",
		SnapshotCopy: "저장소의 소스는 최신 안정판보다 앞설 수 있고, 외부 레지스트리에는 예전에 확인한 버전이 남아 있을 수 있습니다. 그래서 세 값을 따로 표시합니다.",
		SourceLabel:  "소스 패키지", StableLabel: "최신 안정판", RegistryLabel: "MCP Registry에서 확인한 버전",
		MatrixEyebrow: "배포 현황", MatrixTitle: "채널별 제공 상태",
		MatrixCopy:    "아래 연동은 모두 로컬에서 실행됩니다. ‘소스 제공’은 검토한 패키지가 이 저장소에 있다는 뜻이며, 해당 공급자가 마켓에 올렸거나 보증했다는 뜻은 아닙니다.",
		ColumnChannel: "채널", ColumnPackage: "형식", ColumnSurface: "사용 위치", ColumnState: "등록 상태", ColumnVersion: "버전", ColumnPath: "제공 방식", ColumnEvidence: "근거", EvidenceLabel: "확인",
		MeaningEyebrow: "상태 표시", MeaningTitle: "호환, 패키지, 공개는 서로 다른 상태입니다.",
		PublishedTitle: "공개됨", PublishedCopy: "표시된 버전의 외부 공개 기록을 확인했습니다. 안정판 릴리스 자동화가 게시 후 다시 확인합니다.",
		SourceTitle: "소스 제공", SourceCopy: "버전이 붙은 네이티브 패키지를 이 저장소에서 생성하고 테스트합니다. 공급자의 공식 디렉터리에 등록됐다고 주장하지는 않습니다.",
		NotListedTitle: "미등록", NotListedCopy: "corr setup으로 검토된 MCP 직접 설정을 사용할 수 있습니다. 마켓 제출은 네이티브 메타데이터와 상위 검토가 준비된 뒤에 합니다.",
		BoundaryEyebrow: "로컬 실행", BoundaryTitle: "마켓에 등록돼도 호스팅 에이전트나 비공개 터널이 켜지지는 않습니다.",
		BoundaryCopy:   "Corresync는 로컬 stdio 서버와 로컬 계정 상태만 제공합니다. 플러그인 형식이 있더라도 호스팅 ChatGPT, 클라우드 에이전트, Kiro Web, 원격 샌드박스에서는 접근할 수 없습니다.",
		BoundaryDetail: "호스팅 릴레이나 비공개 MCP 터널을 쓰면 데이터 경로가 새로 생깁니다. 그러려면 별도의 아키텍처 결정, 동의, 개인정보 고지, 철회 방법, 보안 검토가 필요합니다.",
		CTAeyebrow:     "시작하기", CTATitle: "corr setup을 실행하면 이 컴퓨터의 에이전트를 찾아 연결합니다.",
		CTACopy: "이 표는 참고용입니다. 처음에는 corr setup부터 실행하세요. 검토된 로컬 연결 방법만 제안합니다.", CTAStart: "시작 안내 열기", CTAProviders: "서비스별 연결 방식 보기",
		FooterCopy:    "독립적으로 개발한 로컬 메일·캘린더·할 일 도구입니다. 이 페이지에 나오는 서비스나 AI 에이전트 업체와 제휴 관계가 없으며 보증을 받지도 않았습니다.",
		FooterPrivacy: "개인정보", FooterTerms: "이용 약관", FooterSource: "소스 코드", FooterSecurity: "보안 문제 신고", FooterReleases: "릴리스",
		NoVersion: "표시 안 함", AutomaticReleasePath: "검증된 릴리스에서 자동 제공", StableOnlyPath: "안정판 태그만", SourcePath: "버전이 붙은 저장소 소스", ManualPath: "직접 설정 가능·목록 미등록", ListSeparator: " · ",
		Packages: sharedPackages("릴리스 압축 파일 + MCPB", "자체 포함 MCPB", "경량 플러그인", "공유 플러그인", "경량 확장", "경량 Power", "직접 MCP 설정"),
		Surfaces: map[string]string{"local-cli": "로컬 CLI", "local-desktop": "로컬 데스크톱", "local-ide": "로컬 IDE"},
		States:   map[string]string{"published": "공개됨", "source-available": "소스 제공", "not-listed": "미등록"},
	}
}
