(function (root, factory) {
  "use strict";

  const checker = factory(root);
  if (typeof module === "object" && module.exports) {
    module.exports = checker;
    return;
  }
  root.CorresyncCompatibilityChecker = checker;
  if (root.document) {
    checker.enhance(root.document, root.fetch.bind(root));
  }
}(typeof globalThis === "object" ? globalThis : this, function (root) {
  "use strict";

  const endpoint = "https://discover.corresync.org/v1/check";
  const maximumResponseBytes = 32 * 1024;
  const routeNames = Object.freeze({
    "microsoft-owa": "Outlook Web",
    "microsoft-graph": "Microsoft Graph",
    "apple-icloud": "iCloud Mail and Calendar",
    google: "Gmail and Google Calendar",
    jmap: "JMAP",
    "imap-smtp": "IMAP / SMTP",
    caldav: "CalDAV",
  });
  const routeStatus = Object.freeze({
    available: "Available now",
    additional_setup: "Available with setup",
    not_available: "Coming soon",
  });
  const routeCapabilities = Object.freeze({
    "microsoft-owa": Object.freeze({
      mail: "Typed mail reads and writes",
      calendar: "Selectable calendars and provider-supported Teams links",
    }),
    "microsoft-graph": Object.freeze({
      mail: "Typed mail reads and writes",
      calendar: "Selectable calendars and typed Teams-link creation",
    }),
    "apple-icloud": Object.freeze({
      mail: "IMAP reads and organization with SMTP composition and sending",
      calendar: "CalDAV calendar reads and reviewed writes",
    }),
    google: Object.freeze({
      mail: "Gmail API support is included but disabled",
      calendar: "Google Calendar and Meet support is included but disabled",
    }),
    jmap: Object.freeze({
      mail: "Typed JMAP mail operations",
      calendar: "Not provided by this route",
    }),
    "imap-smtp": Object.freeze({
      mail: "IMAP reads and organization; SMTP draft and send when both services are available",
      calendar: "Not provided by this route",
    }),
    caldav: Object.freeze({
      mail: "Not provided by this route",
      calendar: "Typed calendar operations and conditional scheduling",
    }),
  });
  const familyNames = Object.freeze({
    "microsoft-consumer": "Outlook.com, Hotmail, or Live",
    "microsoft-365": "Microsoft 365 / Exchange Online (commonly used through Outlook)",
    "google-consumer": "Gmail",
    "google-workspace": "Google Workspace",
    "apple-icloud": "Apple iCloud",
    standards: "Standards-based provider",
    unknown: "Provider not identified",
  });
  const confidenceNames = Object.freeze({
    high: "High confidence",
    medium: "Medium confidence",
    low: "Low confidence",
    unknown: "Confidence unknown",
  });
  const signInNames = Object.freeze({
    provider_browser: "A dedicated visible browser owns sign-in",
    public_oauth: "System-browser OAuth; the grant stays in the OS keyring",
    external_credential: "OS keyring or an explicitly approved credential helper",
    disabled: "Disabled; no sign-in can start",
  });
  const nextLinks = Object.freeze({
    "providers/google": { href: "#google", label: "Read about Google support" },
    "providers/apple-icloud": { href: "#icloud", label: "See iCloud setup" },
    "providers/route-cards": { href: "#route-cards", label: "Compare provider routes" },
    "providers/microsoft-owa": { href: "#microsoft-owa", label: "See Outlook Web" },
    "providers/microsoft-graph": { href: "#microsoft-graph", label: "See Microsoft Graph" },
    "providers/jmap": { href: "#jmap", label: "See JMAP" },
    "providers/imap-smtp": { href: "#imap-smtp", label: "See IMAP and SMTP" },
    "providers/caldav": { href: "#caldav", label: "See CalDAV" },
    "getting-started/install": { href: "getting-started.html#step-install", label: "Install Corresync" },
    "getting-started/sign-in": { href: "getting-started.html#sign-in", label: "See the setup steps" },
  });
  const evidenceNames = Object.freeze({
    known_domain: "Known provider domain",
    mx_provider: "Mail exchange",
    autodiscover_provider: "Autodiscover record",
    srv_imaps: "Secure IMAP service",
    srv_submission: "Mail submission service",
    srv_caldavs: "CalDAV service",
    srv_jmap: "JMAP service",
  });
  const statusCopy = Object.freeze({
    verified: {
      glyph: "✓",
      title: "This address looks ready for Corresync.",
      body: "Public records match a route that is available now. Sign-in and organization policy still decide what the account can do.",
    },
    likely: {
      glyph: "≈",
      title: "This address looks compatible.",
      body: "Public records point to a Corresync route, but the provider and your organization confirm the final capabilities at sign-in.",
    },
    additional_setup: {
      glyph: "+",
      title: "This address may work with a little setup.",
      body: "The domain publishes a standards-based or explicitly configured route. You will need to confirm its endpoint and credential requirements locally.",
    },
    conflict: {
      glyph: "?",
      title: "We found more than one possible route.",
      body: "That can be normal for custom domains. Compare the routes below and choose the one your account administrator supports.",
    },
    not_available: {
      glyph: "…",
      title: "Gmail and Google Calendar are coming soon.",
      body: "The Gmail and Calendar API integration is built and included in RC releases, but it is completely disabled while production OAuth approval is pending. No Google sign-in can start. Until approval, Google’s official Workspace MCP servers are the available interim option.",
    },
    unknown: {
      glyph: "?",
      title: "We could not identify this provider yet.",
      body: "That does not mean the address is unsupported. Public DNS may not advertise enough information, so local discovery or a provider-supplied endpoint is the next step.",
    },
  });

  const englishCopy = Object.freeze({
    routeNames,
    routeStatus,
    familyNames,
    confidenceNames,
    signInNames,
    nextLinks,
    evidenceNames,
    statusCopy,
    routesHeading: "Routes to consider",
    mail: "Mail",
    calendar: "Calendar",
    signIn: "Sign-in",
    isolation: "Add this account separately when you are ready. Its identity and authorization stay isolated from your other work or personal accounts, while combined searches and agendas keep the source account visible.",
    details: "Technical details and limits",
    publicSignals: values => `Public signals: ${values.join(", ")}.`,
    unavailableStatus: "! Check unavailable",
    unavailableTitle: "We could not complete the check.",
    statusLabels: Object.freeze({
      conflict: "More than one route found",
      verified: "Available now",
      likely: "Likely compatible",
      additional_setup: "Additional setup",
      not_available: "Coming soon",
      unknown: "Not identified",
    }),
    checking: "Checking the domain’s public provider records…",
    checkingSlow: "Still checking. Some DNS providers take a little longer to answer.",
    ready: "Compatibility result ready.",
    timedOut: "The check timed out. Nothing was stored; please try again.",
    unavailable: "The compatibility checker is unavailable right now. You can still run local discovery after installing Corresync.",
    unfinished: "The compatibility check could not finish.",
    unexpected: "The lookup service returned an unexpected response.",
    rateLimited: "The checker is busy. Please wait a minute and try again.",
    invalidDomain: "The lookup service did not accept that domain.",
    routeCapabilities,
    serverText: null,
    sentenceSeparator: " ",
  });

  const japaneseCopy = Object.freeze({
    routeNames: Object.freeze({
      "microsoft-owa": "Outlook Web",
      "microsoft-graph": "Microsoft Graph",
      "apple-icloud": "iCloudメール・カレンダー",
      google: "Gmail・Googleカレンダー",
      jmap: "JMAP",
      "imap-smtp": "IMAP / SMTP",
      caldav: "CalDAV",
    }),
    routeStatus: Object.freeze({
      available: "現在利用可能",
      additional_setup: "設定後に利用可能",
      not_available: "近日対応",
    }),
    familyNames: Object.freeze({
      "microsoft-consumer": "Outlook.com、Hotmail、Live",
      "microsoft-365": "Microsoft 365 / Exchange Online（通常はOutlookで利用）",
      "google-consumer": "Gmail",
      "google-workspace": "Google Workspace",
      "apple-icloud": "Apple iCloud",
      standards: "標準プロトコル対応プロバイダー",
      unknown: "プロバイダーを特定できませんでした",
    }),
    confidenceNames: Object.freeze({
      high: "確度：高",
      medium: "確度：中",
      low: "確度：低",
      unknown: "確度：不明",
    }),
    signInNames: Object.freeze({
      provider_browser: "専用の見えるブラウザでサインイン",
      public_oauth: "システムブラウザでOAuth。認可情報はOSキーチェーンに保存",
      external_credential: "OSキーチェーンまたは明示的に許可した認証ヘルパー",
      disabled: "無効。サインインは開始できません",
    }),
    nextLinks: Object.freeze({
      "providers/google": { href: "#google", label: "Google対応について読む" },
      "providers/apple-icloud": { href: "#icloud", label: "iCloudの設定を見る" },
      "providers/route-cards": { href: "#route-cards", label: "利用経路を比較する" },
      "providers/microsoft-owa": { href: "#microsoft-owa", label: "Outlook Webを見る" },
      "providers/microsoft-graph": { href: "#microsoft-graph", label: "Microsoft Graphを見る" },
      "providers/jmap": { href: "#jmap", label: "JMAPを見る" },
      "providers/imap-smtp": { href: "#imap-smtp", label: "IMAP / SMTPを見る" },
      "providers/caldav": { href: "#caldav", label: "CalDAVを見る" },
      "getting-started/install": { href: "getting-started.html#step-install", label: "Corresyncをインストール" },
      "getting-started/sign-in": { href: "getting-started.html#sign-in", label: "設定手順を見る" },
    }),
    evidenceNames: Object.freeze({
      known_domain: "既知のプロバイダードメイン",
      mx_provider: "メール交換サーバー",
      autodiscover_provider: "Autodiscoverレコード",
      srv_imaps: "暗号化IMAPサービス",
      srv_submission: "メール送信サービス",
      srv_caldavs: "CalDAVサービス",
      srv_jmap: "JMAPサービス",
    }),
    statusCopy: Object.freeze({
      verified: { glyph: "✓", title: "このアドレスはCorresyncですぐに使えそうです。", body: "公開情報から、現在利用できる経路を確認できました。実際の機能は、サインイン後の権限と組織のポリシーによって決まります。" },
      likely: { glyph: "≈", title: "このアドレスはCorresyncに対応している可能性が高いです。", body: "公開情報から利用できそうな経路が見つかりました。最終的な対応範囲は、サインイン時にプロバイダーと組織の設定から確認します。" },
      additional_setup: { glyph: "+", title: "少し設定すれば利用できそうです。", body: "標準プロトコルまたは明示設定が必要な経路が公開されています。接続先と認証要件をローカルで確認してください。" },
      conflict: { glyph: "?", title: "複数の利用経路が見つかりました。", body: "独自ドメインでは珍しくありません。候補を比較し、組織の管理者が対応している経路を選んでください。" },
      not_available: { glyph: "…", title: "GmailとGoogleカレンダーは近日対応です。", body: "API連携は実装済みでRCにも含まれますが、本番OAuth承認までは完全に無効です。Googleサインインは開始できません。承認まではGoogle公式Workspace MCPを利用できます。" },
      unknown: { glyph: "?", title: "プロバイダーを特定できませんでした。", body: "未対応という意味ではありません。公開DNSだけでは情報が足りない場合があるため、次はローカル検索か、プロバイダー指定の接続先を確認してください。" },
    }),
    routesHeading: "利用を検討できる経路",
    mail: "メール",
    calendar: "カレンダー",
    signIn: "サインイン",
    isolation: "準備ができたら、このアカウントを個別に追加してください。仕事用や個人用の別アカウントとは本人性と認可を分離したまま、横断検索や予定一覧には元のアカウントが表示されます。",
    details: "判定の根拠と注意点",
    publicSignals: values => `確認した公開情報：${values.join("、")}。`,
    unavailableStatus: "! 現在確認できません",
    unavailableTitle: "対応状況を確認できませんでした。",
    statusLabels: Object.freeze({
      conflict: "複数の経路を検出",
      verified: "現在利用可能",
      likely: "対応の可能性が高い",
      additional_setup: "追加設定が必要",
      not_available: "近日対応",
      unknown: "未特定",
    }),
    checking: "ドメインの公開情報からプロバイダーを確認しています…",
    checkingSlow: "確認を続けています。DNSによっては応答に少し時間がかかります。",
    ready: "対応状況を表示しました。",
    timedOut: "時間内に確認できませんでした。情報は保存していません。もう一度お試しください。",
    unavailable: "現在、オンラインの対応確認を利用できません。Corresyncをインストールした後、ローカル検索は引き続き実行できます。",
    unfinished: "対応状況の確認を完了できませんでした。",
    unexpected: "確認サービスから予期しない応答が返されました。",
    rateLimited: "確認が混み合っています。1分ほど待ってから、もう一度お試しください。",
    invalidDomain: "そのドメインは確認サービスで受け付けられませんでした。",
    routeCapabilities: Object.freeze({
      "microsoft-owa": { mail: "型付きのメール読み取り・書き込み", calendar: "選択可能なカレンダーと、対応時のTeams参加リンク" },
      "microsoft-graph": { mail: "型付きのメール読み取り・書き込み", calendar: "選択可能なカレンダーと型付きTeamsリンク作成" },
      "apple-icloud": { mail: "IMAPでの読み取りと整理、SMTPでの作成と送信", calendar: "CalDAVでの読み取りと確認付き書き込み" },
      google: { mail: "Gmail API対応は実装済みですが無効", calendar: "GoogleカレンダーとMeet対応は実装済みですが無効" },
      jmap: { mail: "型付きJMAPメール操作", calendar: "この経路では提供されません" },
      "imap-smtp": { mail: "IMAPでの読み取り・整理。両サービスが利用可能ならSMTPで下書き・送信", calendar: "この経路では提供されません" },
      caldav: { mail: "この経路では提供されません", calendar: "型付きカレンダー操作と条件付きスケジュール" },
    }),
    serverText: Object.freeze({
      "Organization policy and existing mailbox permissions still apply.": "組織のポリシーと既存のメールボックス権限が適用されます。",
      "Register or use a public OAuth client your organization authorizes.": "組織が許可した公開OAuthクライアントを登録または使用してください。",
      "Graph is explicit and never an automatic fallback.": "Graphは明示的に選ぶ経路で、自動的な代替経路にはなりません。",
      "Enable Apple Account two-factor authentication and create an app-specific password.": "Apple Accountの2ファクタ認証を有効にし、アプリ用パスワードを作成してください。",
      "The guided preset is synthetic-contract covered and remains live-unobserved.": "ガイド付きプリセットは合成契約テスト済みで、実環境では未観測です。",
      "Provider policy and existing mailbox permissions still apply.": "プロバイダーのポリシーと既存のメールボックス権限が適用されます。",
      "Production OAuth approval is pending.": "本番OAuthは承認待ちです。",
      "A separate reviewed release must enable the route.": "有効化には別のレビュー済みリリースが必要です。",
      "Provide a credential handle after local setup confirms the endpoint.": "ローカル設定で接続先を確認した後、認証情報への参照を指定してください。",
      "Server capabilities determine available writes.": "利用できる書き込み操作は、サーバーが実際に提供する機能で決まります。",
      "No IMAPS service record was observed.": "IMAPSのサービスレコードを確認できませんでした。",
      "No mail-submission service record was observed.": "メール送信用のサービスレコードを確認できませんでした。",
      "Confirm both encrypted endpoints and any provider-specific app-password or bridge requirement.": "暗号化された両方の接続先と、プロバイダー固有のアプリパスワードやブリッジ要件を確認してください。",
      "Confirm the exact HTTPS calendar collection and credential requirement.": "正確なHTTPSカレンダーコレクションと認証要件を確認してください。",
      "Attendee scheduling requires server-observed RFC 6638 support.": "参加者を含む予定調整には、サーバーで確認済みのRFC 6638対応が必要です。",
      "Provider detection does not guarantee sign-in or permission.": "プロバイダーを特定できても、サインインや権限を保証するものではありません。",
      "Organization policy, administrator consent, disabled protocols, app passwords, or a bridge may still be required.": "組織ポリシー、管理者同意、無効化されたプロトコル、アプリパスワード、ブリッジなどが必要な場合があります。",
      "Some public DNS signals were unavailable during this lookup.": "確認中、一部の公開DNS情報を取得できませんでした。",
    }),
    sentenceSeparator: "",
  });

  const simplifiedChineseCopy = Object.freeze({
    routeNames: Object.freeze({
      "microsoft-owa": "Outlook Web",
      "microsoft-graph": "Microsoft Graph",
      "apple-icloud": "iCloud 邮件与日历",
      google: "Gmail 和 Google 日历",
      jmap: "JMAP",
      "imap-smtp": "IMAP / SMTP",
      caldav: "CalDAV",
    }),
    routeStatus: Object.freeze({
      available: "现已可用",
      additional_setup: "设置后可用",
      not_available: "即将支持",
    }),
    familyNames: Object.freeze({
      "microsoft-consumer": "Outlook.com、Hotmail 或 Live",
      "microsoft-365": "Microsoft 365 / Exchange Online（通常通过 Outlook 使用）",
      "google-consumer": "Gmail",
      "google-workspace": "Google Workspace",
      "apple-icloud": "Apple iCloud",
      standards: "基于开放标准的服务",
      unknown: "尚未识别服务提供商",
    }),
    confidenceNames: Object.freeze({
      high: "可信度高",
      medium: "可信度中等",
      low: "可信度低",
      unknown: "可信度未知",
    }),
    signInNames: Object.freeze({
      provider_browser: "由专用的可见浏览器管理登录",
      public_oauth: "使用系统浏览器 OAuth；授权保存在系统钥匙串",
      external_credential: "使用系统钥匙串或明确批准的凭据助手",
      disabled: "已关闭；无法启动登录",
    }),
    nextLinks: Object.freeze({
      "providers/google": { href: "#google", label: "了解 Google 支持状态" },
      "providers/apple-icloud": { href: "#icloud", label: "查看 iCloud 设置" },
      "providers/route-cards": { href: "#route-cards", label: "比较连接方式" },
      "providers/microsoft-owa": { href: "#microsoft-owa", label: "查看 Outlook Web" },
      "providers/microsoft-graph": { href: "#microsoft-graph", label: "查看 Microsoft Graph" },
      "providers/jmap": { href: "#jmap", label: "查看 JMAP" },
      "providers/imap-smtp": { href: "#imap-smtp", label: "查看 IMAP / SMTP" },
      "providers/caldav": { href: "#caldav", label: "查看 CalDAV" },
      "getting-started/install": { href: "getting-started.html#step-install", label: "安装 Corresync" },
      "getting-started/sign-in": { href: "getting-started.html#sign-in", label: "查看设置步骤" },
    }),
    evidenceNames: Object.freeze({
      known_domain: "已知服务域名",
      mx_provider: "邮件交换记录",
      autodiscover_provider: "Autodiscover 记录",
      srv_imaps: "加密 IMAP 服务",
      srv_submission: "邮件提交服务",
      srv_caldavs: "CalDAV 服务",
      srv_jmap: "JMAP 服务",
    }),
    statusCopy: Object.freeze({
      verified: { glyph: "✓", title: "这个地址看起来可以立即使用 Corresync。", body: "公共记录与目前可用的连接方式相符。账号最终能做什么，仍由登录后的权限和组织政策决定。" },
      likely: { glyph: "≈", title: "这个地址很可能与 Corresync 兼容。", body: "公共记录指向一条可用路径；最终能力会在登录时由服务和组织设置确认。" },
      additional_setup: { glyph: "+", title: "完成少量设置后应该可以使用。", body: "该域名公开了基于标准或需要明确配置的路径。请在本地确认端点和凭据要求。" },
      conflict: { glyph: "?", title: "找到了多条可能的连接方式。", body: "自定义域名出现这种情况很正常。请比较候选项，并选择组织管理员支持的路径。" },
      not_available: { glyph: "…", title: "Gmail 和 Google 日历即将支持。", body: "API 集成已实现并包含在 RC 中，但生产环境 OAuth 获批前完全关闭。Google 登录无法启动。审批期间可暂时使用 Google 官方 Workspace MCP。" },
      unknown: { glyph: "?", title: "暂时无法识别这项服务。", body: "这并不表示不受支持。公共 DNS 可能没有提供足够信息，下一步可以在本地发现或查看服务商给出的端点。" },
    }),
    routesHeading: "可考虑的连接方式",
    mail: "邮件",
    calendar: "日历",
    signIn: "登录",
    isolation: "准备好后，请把这个账号单独添加。它的身份和授权会与其他工作或个人账号隔离，跨账号搜索和日程仍会标明来源。",
    details: "判断依据与限制",
    publicSignals: values => `已检查的公共信息：${values.join("、")}。`,
    unavailableStatus: "! 暂时无法检查",
    unavailableTitle: "未能完成支持情况检查。",
    statusLabels: Object.freeze({
      conflict: "发现多条路径",
      verified: "现已可用",
      likely: "很可能兼容",
      additional_setup: "需要额外设置",
      not_available: "即将支持",
      unknown: "尚未识别",
    }),
    checking: "正在通过域名的公共记录确认服务提供商…",
    checkingSlow: "仍在检查。部分 DNS 服务需要更长时间才能响应。",
    ready: "支持情况已显示。",
    timedOut: "检查超时。没有保存任何信息，请重试。",
    unavailable: "在线检查目前不可用。安装 Corresync 后仍可运行本地发现。",
    unfinished: "未能完成支持情况检查。",
    unexpected: "检查服务返回了意外响应。",
    rateLimited: "检查服务正忙。请等待一分钟后重试。",
    invalidDomain: "检查服务无法接受这个域名。",
    routeCapabilities: Object.freeze({
      "microsoft-owa": { mail: "类型明确的邮件读取与写入", calendar: "可选择日历，并在支持时使用 Teams 加入链接" },
      "microsoft-graph": { mail: "类型明确的邮件读取与写入", calendar: "可选择日历，并创建类型明确的 Teams 链接" },
      "apple-icloud": { mail: "通过 IMAP 读取和整理，通过 SMTP 撰写和发送", calendar: "通过 CalDAV 读取日历并执行需确认的写入" },
      google: { mail: "Gmail API 支持已实现但尚未启用", calendar: "Google 日历与 Meet 支持已实现但尚未启用" },
      jmap: { mail: "类型明确的 JMAP 邮件操作", calendar: "这条路径不提供日历" },
      "imap-smtp": { mail: "通过 IMAP 读取和整理；两项服务都可用时通过 SMTP 撰写和发送", calendar: "这条路径不提供日历" },
      caldav: { mail: "这条路径不提供邮件", calendar: "类型明确的日历操作和条件式日程调度" },
    }),
    serverText: Object.freeze({
      "Organization policy and existing mailbox permissions still apply.": "组织政策与现有邮箱权限仍然适用。",
      "Register or use a public OAuth client your organization authorizes.": "请注册或使用组织授权的公开 OAuth 客户端。",
      "Graph is explicit and never an automatic fallback.": "Graph 必须明确选择，绝不会自动作为替代路径。",
      "Enable Apple Account two-factor authentication and create an app-specific password.": "请启用 Apple 账号双重认证并创建 App 专用密码。",
      "The guided preset is synthetic-contract covered and remains live-unobserved.": "引导预设已通过合成契约测试，目前尚未进行实机观察。",
      "Provider policy and existing mailbox permissions still apply.": "服务商政策与现有邮箱权限仍然适用。",
      "Production OAuth approval is pending.": "生产环境 OAuth 正在等待审批。",
      "A separate reviewed release must enable the route.": "这条路径只能通过另一次独立审阅的发布启用。",
      "Provide a credential handle after local setup confirms the endpoint.": "本地设置确认端点后，请提供凭据句柄。",
      "Server capabilities determine available writes.": "可执行的写入由服务器实际能力决定。",
      "No IMAPS service record was observed.": "没有发现 IMAPS 服务记录。",
      "No mail-submission service record was observed.": "没有发现邮件提交服务记录。",
      "Confirm both encrypted endpoints and any provider-specific app-password or bridge requirement.": "请确认两个加密端点，以及服务商要求的应用专用密码或桥接程序。",
      "Confirm the exact HTTPS calendar collection and credential requirement.": "请确认准确的 HTTPS 日历集合和凭据要求。",
      "Attendee scheduling requires server-observed RFC 6638 support.": "参与者日程调度需要服务器实际提供 RFC 6638 支持。",
      "Provider detection does not guarantee sign-in or permission.": "识别服务商并不保证能够登录或取得权限。",
      "Organization policy, administrator consent, disabled protocols, app passwords, or a bridge may still be required.": "仍可能需要组织政策、管理员同意、启用协议、应用专用密码或桥接程序。",
      "Some public DNS signals were unavailable during this lookup.": "检查期间有部分公共 DNS 信息不可用。",
    }),
    sentenceSeparator: "",
  });

  const traditionalChineseCopy = Object.freeze({
    routeNames: Object.freeze({
      "microsoft-owa": "Outlook Web",
      "microsoft-graph": "Microsoft Graph",
      "apple-icloud": "iCloud 郵件與行事曆",
      google: "Gmail 和 Google 行事曆",
      jmap: "JMAP",
      "imap-smtp": "IMAP / SMTP",
      caldav: "CalDAV",
    }),
    routeStatus: Object.freeze({ available: "目前可用", additional_setup: "設定後可用", not_available: "即將支援" }),
    familyNames: Object.freeze({
      "microsoft-consumer": "Outlook.com、Hotmail 或 Live",
      "microsoft-365": "Microsoft 365 / Exchange Online（通常透過 Outlook 使用）",
      "google-consumer": "Gmail",
      "google-workspace": "Google Workspace",
      "apple-icloud": "Apple iCloud",
      standards: "採用開放標準的服務",
      unknown: "尚未識別服務供應商",
    }),
    confidenceNames: Object.freeze({ high: "可信度高", medium: "可信度中等", low: "可信度低", unknown: "可信度未知" }),
    signInNames: Object.freeze({
      provider_browser: "由專用的可見瀏覽器管理登入",
      public_oauth: "使用系統瀏覽器 OAuth；授權保存在系統鑰匙圈",
      external_credential: "使用系統鑰匙圈或明確核准的認證資料輔助程式",
      disabled: "已關閉；無法開始登入",
    }),
    nextLinks: Object.freeze({
      "providers/google": { href: "#google", label: "瞭解 Google 支援狀態" },
      "providers/apple-icloud": { href: "#icloud", label: "查看 iCloud 設定" },
      "providers/route-cards": { href: "#route-cards", label: "比較連線方式" },
      "providers/microsoft-owa": { href: "#microsoft-owa", label: "查看 Outlook Web" },
      "providers/microsoft-graph": { href: "#microsoft-graph", label: "查看 Microsoft Graph" },
      "providers/jmap": { href: "#jmap", label: "查看 JMAP" },
      "providers/imap-smtp": { href: "#imap-smtp", label: "查看 IMAP / SMTP" },
      "providers/caldav": { href: "#caldav", label: "查看 CalDAV" },
      "getting-started/install": { href: "getting-started.html#step-install", label: "安裝 Corresync" },
      "getting-started/sign-in": { href: "getting-started.html#sign-in", label: "查看設定步驟" },
    }),
    evidenceNames: Object.freeze({
      known_domain: "已知服務網域", mx_provider: "郵件交換記錄", autodiscover_provider: "Autodiscover 記錄",
      srv_imaps: "加密 IMAP 服務", srv_submission: "郵件提交服務", srv_caldavs: "CalDAV 服務", srv_jmap: "JMAP 服務",
    }),
    statusCopy: Object.freeze({
      verified: { glyph: "✓", title: "這個位址看起來可以立即使用 Corresync。", body: "公開記錄符合目前可用的連線方式。帳號最終能做什麼，仍由登入後的權限和組織政策決定。" },
      likely: { glyph: "≈", title: "這個位址很可能與 Corresync 相容。", body: "公開記錄指向可用路徑；最終功能會在登入時由服務與組織設定確認。" },
      additional_setup: { glyph: "+", title: "完成少量設定後應該可以使用。", body: "這個網域公開了採用標準或需要明確設定的路徑。請在本機確認端點和認證資料需求。" },
      conflict: { glyph: "?", title: "找到多種可能的連線方式。", body: "自訂網域出現這種情況很正常。請比較候選項目，並選擇組織管理員支援的路徑。" },
      not_available: { glyph: "…", title: "Gmail 和 Google 行事曆即將支援。", body: "API 整合已完成並包含在 RC 中，但正式環境 OAuth 通過審核前會完全關閉。Google 登入無法開始。這段期間可暫時使用 Google 官方 Workspace MCP。" },
      unknown: { glyph: "?", title: "目前無法識別這項服務。", body: "這不表示不受支援。公開 DNS 可能沒有足夠資訊，下一步可以執行本機探索或查看服務供應商提供的端點。" },
    }),
    routesHeading: "可考慮的連線方式", mail: "郵件", calendar: "行事曆", signIn: "登入",
    isolation: "準備好後，請將這個帳號個別加入。它的身分與授權會和其他工作或個人帳號隔離，跨帳號搜尋與行程仍會標示來源。",
    details: "判斷依據與限制", publicSignals: values => `已檢查的公開資訊：${values.join("、")}。`,
    unavailableStatus: "! 目前無法檢查", unavailableTitle: "無法完成支援狀態檢查。",
    statusLabels: Object.freeze({ conflict: "找到多種路徑", verified: "目前可用", likely: "很可能相容", additional_setup: "需要額外設定", not_available: "即將支援", unknown: "尚未識別" }),
    checking: "正在從網域的公開記錄確認服務供應商…", checkingSlow: "仍在檢查。部分 DNS 服務需要較長時間回應。", ready: "已顯示支援狀態。",
    timedOut: "檢查逾時。沒有儲存任何資訊，請再試一次。", unavailable: "線上檢查目前無法使用。安裝 Corresync 後仍可執行本機探索。", unfinished: "無法完成支援狀態檢查。",
    unexpected: "檢查服務傳回非預期的回應。", rateLimited: "檢查服務忙碌中。請稍候一分鐘再試。", invalidDomain: "檢查服務無法接受這個網域。",
    routeCapabilities: Object.freeze({
      "microsoft-owa": { mail: "型別明確的郵件讀取與寫入", calendar: "可選擇行事曆，並在支援時使用 Teams 加入連結" },
      "microsoft-graph": { mail: "型別明確的郵件讀取與寫入", calendar: "可選擇行事曆，並建立型別明確的 Teams 連結" },
      "apple-icloud": { mail: "透過 IMAP 讀取和整理，透過 SMTP 撰寫和傳送", calendar: "透過 CalDAV 讀取行事曆並執行需確認的寫入" },
      google: { mail: "Gmail API 支援已完成但尚未啟用", calendar: "Google 行事曆與 Meet 支援已完成但尚未啟用" },
      jmap: { mail: "型別明確的 JMAP 郵件操作", calendar: "這條路徑不提供行事曆" },
      "imap-smtp": { mail: "透過 IMAP 讀取和整理；兩項服務都可用時透過 SMTP 撰寫和傳送", calendar: "這條路徑不提供行事曆" },
      caldav: { mail: "這條路徑不提供郵件", calendar: "型別明確的行事曆操作與條件式排程" },
    }),
    serverText: Object.freeze({
      "Organization policy and existing mailbox permissions still apply.": "組織政策與現有信箱權限仍然適用。",
      "Register or use a public OAuth client your organization authorizes.": "請註冊或使用組織授權的公開 OAuth 用戶端。",
      "Graph is explicit and never an automatic fallback.": "Graph 必須明確選擇，絕不會自動成為替代路徑。",
      "Enable Apple Account two-factor authentication and create an app-specific password.": "請啟用 Apple 帳號雙重認證並建立 App 專用密碼。",
      "The guided preset is synthetic-contract covered and remains live-unobserved.": "引導預設已通過合成契約測試，目前尚未進行實機觀察。",
      "Provider policy and existing mailbox permissions still apply.": "服務供應商政策與現有信箱權限仍然適用。",
      "Production OAuth approval is pending.": "正式環境 OAuth 正在等待審核。",
      "A separate reviewed release must enable the route.": "這條路徑只能透過另一次獨立審閱的版本啟用。",
      "Provide a credential handle after local setup confirms the endpoint.": "本機設定確認端點後，請提供認證資料參照。",
      "Server capabilities determine available writes.": "可執行的寫入由伺服器實際功能決定。",
      "No IMAPS service record was observed.": "沒有發現 IMAPS 服務記錄。",
      "No mail-submission service record was observed.": "沒有發現郵件提交服務記錄。",
      "Confirm both encrypted endpoints and any provider-specific app-password or bridge requirement.": "請確認兩個加密端點，以及服務供應商要求的應用程式密碼或橋接程式。",
      "Confirm the exact HTTPS calendar collection and credential requirement.": "請確認正確的 HTTPS 行事曆集合和認證資料需求。",
      "Attendee scheduling requires server-observed RFC 6638 support.": "與會者排程需要伺服器實際提供 RFC 6638 支援。",
      "Provider detection does not guarantee sign-in or permission.": "識別服務供應商不保證能夠登入或取得權限。",
      "Organization policy, administrator consent, disabled protocols, app passwords, or a bridge may still be required.": "仍可能需要組織政策、管理員同意、啟用通訊協定、應用程式密碼或橋接程式。",
      "Some public DNS signals were unavailable during this lookup.": "檢查期間有部分公開 DNS 資訊無法使用。",
    }),
    sentenceSeparator: "",
  });

  const koreanCopy = Object.freeze({
    routeNames: Object.freeze({
      "microsoft-owa": "Outlook Web", "microsoft-graph": "Microsoft Graph", google: "Gmail 및 Google 캘린더",
      "apple-icloud": "iCloud Mail과 캘린더", jmap: "JMAP", "imap-smtp": "IMAP / SMTP", caldav: "CalDAV",
    }),
    routeStatus: Object.freeze({ available: "지금 사용 가능", additional_setup: "설정 후 사용 가능", not_available: "곧 지원" }),
    familyNames: Object.freeze({
      "microsoft-consumer": "Outlook.com, Hotmail 또는 Live",
      "microsoft-365": "Microsoft 365 / Exchange Online(일반적으로 Outlook에서 사용)",
      "google-consumer": "Gmail", "google-workspace": "Google Workspace", "apple-icloud": "Apple iCloud", standards: "개방형 표준 서비스", unknown: "서비스 제공자를 찾지 못함",
    }),
    confidenceNames: Object.freeze({ high: "신뢰도 높음", medium: "신뢰도 보통", low: "신뢰도 낮음", unknown: "신뢰도 알 수 없음" }),
    signInNames: Object.freeze({
      provider_browser: "전용으로 보이는 브라우저가 로그인을 관리", public_oauth: "시스템 브라우저 OAuth, 권한은 운영체제 키체인에 보관",
      external_credential: "운영체제 키체인 또는 명시적으로 승인한 자격 증명 도우미", disabled: "비활성, 로그인 시작 불가",
    }),
    nextLinks: Object.freeze({
      "providers/google": { href: "#google", label: "Google 지원 현황 보기" }, "providers/route-cards": { href: "#route-cards", label: "연결 방식 비교" },
      "providers/apple-icloud": { href: "#icloud", label: "iCloud 설정 보기" },
      "providers/microsoft-owa": { href: "#microsoft-owa", label: "Outlook Web 보기" }, "providers/microsoft-graph": { href: "#microsoft-graph", label: "Microsoft Graph 보기" },
      "providers/jmap": { href: "#jmap", label: "JMAP 보기" }, "providers/imap-smtp": { href: "#imap-smtp", label: "IMAP / SMTP 보기" },
      "providers/caldav": { href: "#caldav", label: "CalDAV 보기" }, "getting-started/install": { href: "getting-started.html#step-install", label: "Corresync 설치" },
      "getting-started/sign-in": { href: "getting-started.html#sign-in", label: "설정 방법 보기" },
    }),
    evidenceNames: Object.freeze({
      known_domain: "알려진 서비스 도메인", mx_provider: "메일 교환 레코드", autodiscover_provider: "Autodiscover 레코드",
      srv_imaps: "암호화 IMAP 서비스", srv_submission: "메일 제출 서비스", srv_caldavs: "CalDAV 서비스", srv_jmap: "JMAP 서비스",
    }),
    statusCopy: Object.freeze({
      verified: { glyph: "✓", title: "이 주소는 Corresync에서 바로 사용할 수 있을 것 같습니다.", body: "공개 레코드가 현재 사용할 수 있는 연결 방식과 일치합니다. 최종 기능은 로그인 후 권한과 조직 정책에 따라 달라집니다." },
      likely: { glyph: "≈", title: "이 주소는 Corresync와 호환될 가능성이 높습니다.", body: "공개 레코드에서 사용할 수 있는 경로를 찾았습니다. 최종 기능은 로그인할 때 서비스와 조직 설정에서 확인합니다." },
      additional_setup: { glyph: "+", title: "약간의 설정을 거치면 사용할 수 있을 것 같습니다.", body: "도메인에서 표준 기반 또는 명시적 설정이 필요한 경로를 공개했습니다. 로컬에서 엔드포인트와 자격 증명 요건을 확인하세요." },
      conflict: { glyph: "?", title: "가능한 연결 방식을 두 개 이상 찾았습니다.", body: "사용자 지정 도메인에서는 정상적인 상황일 수 있습니다. 후보를 비교하고 조직 관리자가 지원하는 경로를 고르세요." },
      not_available: { glyph: "…", title: "Gmail과 Google 캘린더는 곧 지원됩니다.", body: "API 통합은 구현되어 RC에 포함됐지만 운영 환경 OAuth 승인 전까지 완전히 꺼져 있습니다. Google 로그인을 시작할 수 없습니다. 그동안은 Google 공식 Workspace MCP를 사용할 수 있습니다." },
      unknown: { glyph: "?", title: "아직 서비스 제공자를 찾지 못했습니다.", body: "지원하지 않는다는 뜻은 아닙니다. 공개 DNS 정보가 부족할 수 있으므로 로컬 탐색을 실행하거나 제공자가 안내한 엔드포인트를 확인하세요." },
    }),
    routesHeading: "고려할 연결 방식", mail: "메일", calendar: "캘린더", signIn: "로그인",
    isolation: "준비되면 이 계정을 별도로 추가하세요. 신원과 권한은 다른 업무용 또는 개인용 계정과 분리되며, 통합 검색과 일정에는 출처 계정이 표시됩니다.",
    details: "판단 근거와 제한", publicSignals: values => `확인한 공개 정보: ${values.join(", ")}.`, unavailableStatus: "! 지금 확인할 수 없음", unavailableTitle: "지원 여부 확인을 완료하지 못했습니다.",
    statusLabels: Object.freeze({ conflict: "경로를 두 개 이상 찾음", verified: "지금 사용 가능", likely: "호환 가능성 높음", additional_setup: "추가 설정 필요", not_available: "곧 지원", unknown: "찾지 못함" }),
    checking: "도메인의 공개 레코드에서 서비스 제공자를 확인하고 있습니다…", checkingSlow: "계속 확인하고 있습니다. 일부 DNS 서비스는 응답에 시간이 더 걸립니다.", ready: "지원 여부를 표시했습니다.",
    timedOut: "확인 시간이 초과됐습니다. 아무 정보도 저장하지 않았습니다. 다시 시도해 주세요.", unavailable: "온라인 지원 확인을 지금 사용할 수 없습니다. Corresync 설치 후 로컬 탐색은 계속 사용할 수 있습니다.", unfinished: "지원 여부 확인을 완료하지 못했습니다.",
    unexpected: "확인 서비스에서 예상하지 못한 응답을 받았습니다.", rateLimited: "확인 요청이 많습니다. 1분 뒤 다시 시도해 주세요.", invalidDomain: "확인 서비스에서 이 도메인을 받을 수 없습니다.",
    routeCapabilities: Object.freeze({
      "microsoft-owa": { mail: "명확히 정의된 메일 읽기와 쓰기", calendar: "캘린더 선택과 지원되는 경우 Teams 참여 링크" },
      "microsoft-graph": { mail: "명확히 정의된 메일 읽기와 쓰기", calendar: "캘린더 선택과 명확히 정의된 Teams 링크 생성" },
      "apple-icloud": { mail: "IMAP 읽기와 정리, SMTP 작성과 전송", calendar: "CalDAV 캘린더 읽기와 승인 후 쓰기" },
      google: { mail: "Gmail API 지원은 구현됐지만 비활성", calendar: "Google 캘린더와 Meet 지원은 구현됐지만 비활성" },
      jmap: { mail: "명확히 정의된 JMAP 메일 작업", calendar: "이 경로에서는 제공하지 않음" },
      "imap-smtp": { mail: "IMAP 읽기와 정리, 두 서비스가 모두 가능하면 SMTP 작성과 전송", calendar: "이 경로에서는 제공하지 않음" },
      caldav: { mail: "이 경로에서는 제공하지 않음", calendar: "명확히 정의된 캘린더 작업과 조건부 일정 조율" },
    }),
    serverText: Object.freeze({
      "Organization policy and existing mailbox permissions still apply.": "조직 정책과 기존 메일함 권한은 계속 적용됩니다.",
      "Register or use a public OAuth client your organization authorizes.": "조직에서 허용한 공개 OAuth 클라이언트를 등록하거나 사용하세요.",
      "Graph is explicit and never an automatic fallback.": "Graph는 명시적으로 선택해야 하며 자동 대체 경로가 아닙니다.",
      "Enable Apple Account two-factor authentication and create an app-specific password.": "Apple 계정 이중 인증을 켜고 앱 암호를 만드세요.",
      "The guided preset is synthetic-contract covered and remains live-unobserved.": "안내 프리셋은 합성 계약 테스트를 통과했으며 아직 실제 환경에서 관찰되지 않았습니다.",
      "Provider policy and existing mailbox permissions still apply.": "서비스 제공자 정책과 기존 메일함 권한은 계속 적용됩니다.",
      "Production OAuth approval is pending.": "운영 환경 OAuth 승인을 기다리고 있습니다.",
      "A separate reviewed release must enable the route.": "이 경로는 별도로 검토한 릴리스에서만 활성화할 수 있습니다.",
      "Provide a credential handle after local setup confirms the endpoint.": "로컬 설정에서 엔드포인트를 확인한 뒤 자격 증명 핸들을 제공하세요.",
      "Server capabilities determine available writes.": "사용할 수 있는 쓰기 작업은 서버가 실제로 제공하는 기능에 따라 결정됩니다.",
      "No IMAPS service record was observed.": "IMAPS 서비스 레코드를 찾지 못했습니다.",
      "No mail-submission service record was observed.": "메일 제출 서비스 레코드를 찾지 못했습니다.",
      "Confirm both encrypted endpoints and any provider-specific app-password or bridge requirement.": "암호화된 두 엔드포인트와 서비스별 앱 비밀번호 또는 브리지 요건을 확인하세요.",
      "Confirm the exact HTTPS calendar collection and credential requirement.": "정확한 HTTPS 캘린더 컬렉션과 자격 증명 요건을 확인하세요.",
      "Attendee scheduling requires server-observed RFC 6638 support.": "참석자 일정 조율에는 서버에서 실제로 확인한 RFC 6638 지원이 필요합니다.",
      "Provider detection does not guarantee sign-in or permission.": "서비스 제공자를 찾았다고 해서 로그인이나 권한이 보장되지는 않습니다.",
      "Organization policy, administrator consent, disabled protocols, app passwords, or a bridge may still be required.": "조직 정책, 관리자 동의, 프로토콜 활성화, 앱 비밀번호 또는 브리지가 필요할 수 있습니다.",
      "Some public DNS signals were unavailable during this lookup.": "확인 중 일부 공개 DNS 정보를 사용할 수 없었습니다.",
    }),
    sentenceSeparator: " ",
  });

  function messagesForLanguage(language) {
    const copies = {
      ja: japaneseCopy,
      "zh-Hans": simplifiedChineseCopy,
      "zh-Hant": traditionalChineseCopy,
      ko: koreanCopy,
    };
    return Object.hasOwn(copies, language) ? copies[language] : englishCopy;
  }

  function copyFor(document) {
    return messagesForLanguage(document.documentElement?.lang);
  }

  function normalizeDomain(value) {
    if (typeof value !== "string" || value === "" || value !== value.trim() ||
      value.length > 253 || value.includes("@") || /[\s\u0000-\u001f%\\/:?#\[\]]/.test(value)) {
      return "";
    }
    let hostname;
    try {
      hostname = new URL(`https://${value.toLowerCase()}`).hostname;
    } catch (_) {
      return "";
    }
    if (!hostname.includes(".") || /^\d+(?:\.\d+){3}$/.test(hostname) || hostname.includes(":")) {
      return "";
    }
    const labels = hostname.split(".");
    if (labels.some(label => label.length < 1 || label.length > 63 ||
      !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(label))) {
      return "";
    }
    return hostname;
  }

  function normalizeEmailDomain(value) {
    if (typeof value !== "string" || value !== value.trim() || /\s/.test(value)) {
      return "";
    }
    const separator = value.lastIndexOf("@");
    if (separator < 1 || separator !== value.indexOf("@") || separator === value.length - 1) {
      return "";
    }
    return normalizeDomain(value.slice(separator + 1));
  }

  function buildLookupRequest(domain) {
    const normalized = normalizeDomain(domain);
    if (!normalized) {
      throw new TypeError("a normalized domain is required");
    }
    return [endpoint, {
      method: "POST",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: JSON.stringify({ domain: normalized }),
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      referrerPolicy: "no-referrer",
    }];
  }

  function enhance(document, fetcher) {
    const form = document.getElementById("compatibility-form");
    if (!form || typeof fetcher !== "function") {
      return;
    }
    const input = document.getElementById("compatibility-email");
    const button = document.getElementById("compatibility-submit");
    const validation = document.getElementById("compatibility-validation");
    const live = document.getElementById("compatibility-live");
    const result = document.getElementById("compatibility-result");
    const copy = copyFor(document);
    let activeController;
    let requestSequence = 0;
    form.addEventListener("submit", async event => {
      event.preventDefault();
      const domain = normalizeEmailDomain(input.value);
      if (!domain) {
        validation.hidden = false;
        input.setAttribute("aria-invalid", "true");
        input.focus();
        return;
      }
      input.value = "";
      input.removeAttribute("aria-invalid");
      validation.hidden = true;
      result.hidden = true;
      button.disabled = true;
      activeController?.abort();
      const controller = new AbortController();
      activeController = controller;
      const sequence = ++requestSequence;
      live.textContent = copy.checking;
      const slowTimer = root.setTimeout(() => {
        if (sequence === requestSequence) {
          live.textContent = copy.checkingSlow;
        }
      }, 2_500);
      const timeoutTimer = root.setTimeout(() => controller.abort(), 8_000);
      try {
        const [url, options] = buildLookupRequest(domain);
        const response = await fetcher(url, { ...options, signal: controller.signal });
        const payload = await readResponse(response, copy);
        if (!response.ok) {
          throw new CheckerError(errorMessage(payload?.error?.code, copy));
        }
        renderResult(document, result, validateResult(payload, copy), copy);
        result.hidden = false;
        result.focus();
        live.textContent = copy.ready;
      } catch (error) {
        if (sequence !== requestSequence) {
          return;
        }
        const message = error instanceof CheckerError
          ? error.message
          : error?.name === "AbortError"
            ? copy.timedOut
            : copy.unavailable;
        renderUnavailable(document, result, message, copy);
        result.hidden = false;
        result.focus();
        live.textContent = copy.unfinished;
      } finally {
        root.clearTimeout(slowTimer);
        root.clearTimeout(timeoutTimer);
        if (sequence === requestSequence) {
          button.disabled = false;
          activeController = undefined;
        }
      }
    });
    button.disabled = false;
  }

  async function readResponse(response, copy = englishCopy) {
    const declaredLength = Number(response.headers.get("Content-Length") || "0");
    if (!Number.isFinite(declaredLength) || declaredLength < 0 ||
      declaredLength > maximumResponseBytes || !response.body) {
      throw new CheckerError(copy.unexpected);
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder("utf-8", { fatal: true });
    let bytes = 0;
    let raw = "";
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        bytes += value.byteLength;
        if (bytes > maximumResponseBytes) {
          throw new CheckerError(copy.unexpected);
        }
        raw += decoder.decode(value, { stream: true });
      }
      raw += decoder.decode();
      return JSON.parse(raw);
    } catch (_) {
      throw new CheckerError(copy.unexpected);
    } finally {
      reader.releaseLock();
    }
  }

  function validateResult(value, copy = englishCopy) {
    if (!plainObject(value) || value.schemaVersion !== 1 ||
      normalizeDomain(value.normalizedDomain) !== value.normalizedDomain ||
      !plainObject(value.classification) || typeof value.classification.conflict !== "boolean" ||
      !Object.hasOwn(familyNames, value.classification.variant) ||
      !Object.hasOwn(confidenceNames, value.classification.confidence) ||
      !["verified", "likely", "additional_setup", "not_available", "unknown"].includes(
        value.classification.status,
      ) || !Array.isArray(value.routes) || value.routes.length > 8 ||
      !Array.isArray(value.caveats) || value.caveats.length > 8 ||
      !Array.isArray(value.evidence) || value.evidence.length > 16 ||
      !Array.isArray(value.next) || value.next.length > 3) {
      throw new CheckerError(copy.unexpected);
    }
    for (const route of value.routes) {
      if (!plainObject(route) || !Object.hasOwn(routeNames, route.provider) ||
        !Object.hasOwn(routeStatus, route.status) || !plainObject(route.capabilities) ||
        !safeText(route.capabilities.mail, 200) || !safeText(route.capabilities.calendar, 200) ||
        !plainObject(route.signIn) || !Object.hasOwn(signInNames, route.signIn.owner) ||
        !Array.isArray(route.requirements) || route.requirements.length > 6 ||
        !Array.isArray(route.caveats) || route.caveats.length > 6 ||
        !route.requirements.every(item => safeText(item, 200)) ||
        !route.caveats.every(item => safeText(item, 200))) {
        throw new CheckerError(copy.unexpected);
      }
    }
    if (!value.caveats.every(item => safeText(item, 240)) ||
      !value.evidence.every(item => plainObject(item) && Object.hasOwn(evidenceNames, item.category)) ||
      !value.next.every(item => plainObject(item) && Object.hasOwn(nextLinks, item.target))) {
      throw new CheckerError(copy.unexpected);
    }
    return value;
  }

  function renderResult(document, result, value, copy = englishCopy) {
    clear(result);
    const status = value.classification.conflict
      ? copy.statusCopy.conflict
      : copy.statusCopy[value.classification.status];
    result.append(
      element(document, "p", "checker-result-status", `${status.glyph} ${statusLabel(value, copy)}`),
      element(document, "h3", "checker-result-title", status.title),
      element(
        document,
        "p",
        "checker-result-family",
        `${copy.familyNames[value.classification.variant]} · ${copy.confidenceNames[value.classification.confidence]}`,
      ),
      element(document, "p", "checker-result-body", status.body),
    );
    if (value.routes.length > 0) {
      const heading = element(document, "h4", "checker-subtitle", copy.routesHeading);
      const list = element(document, "div", "checker-route-list");
      for (const route of value.routes) {
        const item = element(document, "section", "checker-route");
        const headingRow = element(document, "div", "checker-route-heading");
        headingRow.append(
          element(document, "h5", "", copy.routeNames[route.provider]),
          element(document, "span", "chip", copy.routeStatus[route.status]),
        );
        const facts = document.createElement("dl");
        const capabilities = localizedCapabilities(route, copy);
        appendFact(document, facts, copy.mail, capabilities.mail);
        appendFact(document, facts, copy.calendar, capabilities.calendar);
        appendFact(document, facts, copy.signIn, copy.signInNames[route.signIn.owner]);
        item.append(headingRow, facts);
        if (route.requirements.length > 0) {
          item.append(element(document, "p", "checker-route-note", localizedText(route.requirements, copy)));
        }
        if (route.caveats.length > 0) {
          item.append(element(document, "p", "checker-route-note", localizedText(route.caveats, copy)));
        }
        list.append(item);
      }
      result.append(heading, list);
    }
    result.append(element(
      document,
      "p",
      "checker-isolation",
      copy.isolation,
    ));
    if (value.caveats.length > 0 || value.evidence.length > 0) {
      const details = document.createElement("details");
      details.className = "checker-details";
      details.append(element(document, "summary", "", copy.details));
      if (value.evidence.length > 0) {
        details.append(element(
          document,
          "p",
          "",
          copy.publicSignals(value.evidence.map(item => copy.evidenceNames[item.category])),
        ));
      }
      for (const caveat of value.caveats) {
        details.append(element(document, "p", "", localizedServerText(caveat, copy)));
      }
      result.append(details);
    }
    appendNextLinks(document, result, value.next, copy);
  }

  function renderUnavailable(document, result, message, copy = englishCopy) {
    clear(result);
    result.append(
      element(document, "p", "checker-result-status", copy.unavailableStatus),
      element(document, "h3", "checker-result-title", copy.unavailableTitle),
      element(document, "p", "checker-result-body", message),
    );
    appendNextLinks(document, result, [
      { target: "getting-started/install" },
      { target: "providers/route-cards" },
    ], copy);
  }

  function appendNextLinks(document, result, targets, copy = englishCopy) {
    const actions = element(document, "div", "checker-result-actions");
    for (const item of targets) {
      const definition = copy.nextLinks[item.target];
      const link = element(document, "a", "button secondary", definition.label);
      link.href = definition.href;
      actions.append(link);
    }
    result.append(actions);
  }

  function statusLabel(value, copy = englishCopy) {
    if (value.classification.conflict) return copy.statusLabels.conflict;
    return copy.statusLabels[value.classification.status];
  }

  function errorMessage(code, copy = englishCopy) {
    if (code === "rate_limited") return copy.rateLimited;
    if (code === "invalid_domain") return copy.invalidDomain;
    return copy.unavailable;
  }

  function localizedText(values, copy) {
    return values.map(value => localizedServerText(value, copy)).join(copy.sentenceSeparator);
  }

  function localizedCapabilities(route, copy) {
    const canonical = routeCapabilities[route.provider];
    const localized = copy.routeCapabilities?.[route.provider];
    if (!canonical || !localized || route.capabilities.mail !== canonical.mail ||
      route.capabilities.calendar !== canonical.calendar) {
      return route.capabilities;
    }
    return localized;
  }

  function localizedServerText(value, copy) {
    return copy.serverText && Object.hasOwn(copy.serverText, value) ? copy.serverText[value] : value;
  }

  function element(document, tag, className, content) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (content !== undefined) node.textContent = content;
    return node;
  }

  function appendFact(document, list, label, value) {
    const row = document.createElement("div");
    row.append(
      element(document, "dt", "", label),
      element(document, "dd", "", value),
    );
    list.append(row);
  }

  function clear(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  function safeText(value, maximum) {
    return typeof value === "string" && value.length > 0 && value.length <= maximum &&
      !/[\u0000-\u001f\u007f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/.test(value);
  }

  function plainObject(value) {
    return value !== null && typeof value === "object" && !Array.isArray(value) &&
      Object.getPrototypeOf(value) === Object.prototype;
  }

  class CheckerError extends Error {}

  return Object.freeze({
    buildLookupRequest,
    enhance,
    messagesForLanguage,
    normalizeDomain,
    normalizeEmailDomain,
  });
}));
