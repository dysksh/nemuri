# Nemuri

Discordのスラッシュコマンドで自然言語のタスクを投げると、LLM（Claude API）が自律的に実行し、GitHub PR・S3ファイル・Discordメッセージとして結果を返すタスク自動化エージェントシステム。

## アーキテクチャ

```
Discord (slash command)
  → API Gateway
  → Lambda (Ingress): ジョブ作成・SQSへエンキュー
  → SQS
  → Lambda (Runner): ECSタスク起動
  → ECS Fargate (1ジョブ = 1タスク)
    → Agent Engine (Go)
       ├── Claude API (LLM推論)
       ├── DynamoDB (状態管理)
       ├── GitHub (コード成果物・PR作成)
       ├── S3 (中間ファイル・最終成果物)
       └── Discord API (通知・質問)
```

常時起動のインフラはなく、すべてオンデマンド実行。

## 技術スタック

| カテゴリ | 技術 |
|---|---|
| 言語 | Go |
| インフラ | AWS (ECS Fargate, Lambda, SQS, DynamoDB, S3, API Gateway) |
| IaC | Terraform (モジュール分割・環境分離) |
| LLM | Claude API |
| インターフェース | Discord スラッシュコマンド |
| コンテナ | debian:12-slim + Go バイナリ + wkhtmltopdf |

## プロジェクト構成

```
nemuri/
├── cmd/
│   ├── agent-engine/        # ECS上で動くエージェント本体
│   ├── lambda-ingress/      # Discord → SQS
│   └── lambda-runner/       # SQS → ECS RunTask
├── internal/
│   ├── agent/               # Agent loop, Reviewer, Rewriter, prompts
│   ├── llm/                 # LLMアダプタ (Claude API実装)
│   ├── state/               # DynamoDB状態管理・状態遷移
│   ├── converter/           # Markdown → HTML → PDF 変換
│   ├── discord/             # Discord API クライアント
│   ├── github/              # GitHub API クライアント
│   ├── secrets/             # AWS Secrets Manager
│   └── storage/             # S3操作
├── terraform/
│   ├── envs/
│   │   ├── dev/
│   │   └── prod/
│   └── modules/
│       ├── network/         # VPC, サブネット, NAT, SG
│       ├── ecr/             # コンテナイメージリポジトリ
│       ├── ecs/             # クラスタ, タスク定義
│       ├── sqs/             # ジョブキュー + DLQ
│       ├── lambda_ingress/  # Ingress Lambda
│       ├── lambda_runner/   # Runner Lambda
│       ├── dynamodb/        # Jobsテーブル
│       ├── s3/              # アーティファクト・出力バケット
│       └── iam/             # IAMポリシー
├── scripts/                 # ビルド・デプロイスクリプト
├── Dockerfile
├── Makefile
├── SPEC.md                  # 詳細仕様
├── PLAN.md                  # 実装計画
├── TODO.md                  # タスク一覧
└── KNOWLEDGE.md             # 設計判断の記録
```

## 前提条件

- Docker
- AWS アカウント（IAM ユーザーまたはロールの認証情報を設定済み）
- Anthropic API Key を取得済み（[Anthropic Console](https://console.anthropic.com/)）
- GitHub Fine-grained Personal Access Token を取得済み（Administration / Contents / Pull Requests の Read and Write 権限）
- Discord Developer Portal でアプリケーション作成済み
  - **Application ID** / **Bot Token** / **Public Key** を控えておく
  - Bot をサーバーに招待済み（権限: メッセージを送る・公開スレッドを作成・スレッドでメッセージを送る）

## 環境構築

### 1. Discord アプリ & Bot の作成（初回のみ）

[Discord Developer Portal](https://discord.com/developers/applications) で以下を行う:

1. **New Application** でアプリを作成
2. **General Information** から `APPLICATION ID` と `PUBLIC KEY` を控える
3. **Bot** タブで Bot を作成し、`TOKEN` を控える
4. **OAuth2 > URL Generator** で `bot` スコープを選択し、Bot Permissions で「Send Messages」「Create Public Threads」「Send Messages in Threads」を付与して、生成した URL でサーバーに招待

### 2. リポジトリのクローンと開発コンテナの起動

`make up` の前に `aws configure` で AWS 認証情報を設定しておくこと（初回起動時に tfstate 用 S3 バケットの作成が実行される）。

```bash
aws configure     # Access Key ID, Secret Access Key, Region を設定
git clone <repository-url>
cd nemuri
make up        # UID/GID を自動検出して開発コンテナ起動
make dev       # コンテナ内にシェル接続
```

`make up` はホストの UID/GID を自動検出してコンテナに渡すため、ファイル所有権の問題が発生しない。初回起動時に `make setup`（Git hooks + `go mod download`）と `make bootstrap`（tfstate 用 S3 バケット作成）が自動実行される。

VS Code の場合は「Reopen in Container」でも起動可能。

### 3. 環境変数・Terraform 変数の設定（初回のみ）

`.env` を作成（`make deploy` でシークレット登録・コマンド登録に使用）:

```bash
cp .env.example .env
# 各値を埋める:
export DISCORD_APP_ID=
export DISCORD_BOT_TOKEN=
export ANTHROPIC_API_KEY=
export GITHUB_PAT=
```

`terraform/envs/dev/terraform.tfvars` を作成:

```hcl
environment          = "dev"
project              = "nemuri"
aws_region           = "ap-northeast-1"
discord_public_key   = "..."   # Discord Developer Portal > General Information > PUBLIC KEY
default_github_owner = "..."   # GitHub のユーザー名 or Organization 名
```

### 4. デプロイ

```bash
make deploy
```

これだけで以下がすべて実行される:

| ステップ | make ターゲット | 内容 |
|---|---|---|
| 1 | `build-lambda` | Lambda 関数のビルド |
| 2 | `terraform-apply` | `terraform init` + `terraform apply -auto-approve` |
| 3 | `build-and-push-ecr` | Docker イメージのビルド・ECR プッシュ |
| 4 | `put-secret` | `.env` から AWS Secrets Manager にシークレット登録 |
| 5 | `register-commands` | Discord スラッシュコマンド `/agent` を登録 |
| 6 | `register-endpoint` | Terraform output から Interactions URL を取得し、Discord API で自動設定 |

各ステップは個別に `make <ターゲット>` でも実行可能。

### 5. 動作確認

Discord サーバーで `/agent` スラッシュコマンドを実行する。

### セットアップの全体像

```
1. Discord Developer Portal でアプリ & Bot 作成    ← 手動（初回のみ）
2. git clone && make up && make dev                ← コマンド（bootstrap 自動実行）
3. .env と terraform.tfvars を作成                  ← 手動（初回のみ）
4. make deploy                                     ← コマンド（これだけで全自動）
5. /agent で動作確認                                ← Discord
```

### ツールバージョン

| ツール | バージョン |
|---|---|
| Go | 1.25.0 |
| Terraform | 1.14.6 |
| golangci-lint | 2.11.2 |
| tflint | 0.61.0 |
| AWS CLI | v2 |

開発コンテナを使わずホスト上で直接作業する場合は、上記を個別にインストールし、`make setup` と `make bootstrap` を手動で実行する。

### 開発時コマンド

| コマンド | 内容 |
|---|---|
| `make test` | 全パッケージのテスト実行（internal + lambda-ingress + lambda-runner） |
| `make lint` | gofmt + golangci-lint + terraform fmt + tflint |
| `make check` | infracost + terraform plan でデプロイ前確認 |
| `make deploy` | ビルド → インフラ構築 → シークレット登録 → Discord 設定を一括実行 |
| `make build-lambda` | Lambda 関数のビルド |
| `make build-and-push-ecr` | Docker イメージのビルド・ECR プッシュ |
| `make terraform-apply` | Terraform の適用 |
| `make put-secret` | `.env` のシークレットを Secrets Manager に登録 |
| `make register-commands` | Discord スラッシュコマンドの登録 |
| `make register-endpoint` | Discord Interactions URL の自動設定 |
| `make bootstrap` | tfstate 用 S3 バケットの作成（初回のみ） |

## 関連ドキュメント

| ファイル | 内容 |
|---|---|
| [SPEC.md](SPEC.md) | システム仕様・アーキテクチャ詳細 |
| [PLAN.md](PLAN.md) | フェーズ別実装計画 |
| [TODO.md](TODO.md) | タスク一覧・進捗管理 |
| [KNOWLEDGE.md](KNOWLEDGE.md) | 設計判断の背景と理由 |

## ライセンス

Private
