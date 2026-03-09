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
│   ├── agent/               # Planner, Builder, Reviewer, Rewriter
│   ├── llm/                 # LLMアダプタ (Claude API実装)
│   ├── state/               # DynamoDB状態管理・状態遷移
│   ├── tools/               # ツール実行 (GitHub, S3, Discord)
│   ├── worker/              # ジョブライフサイクル・ハートビート
│   ├── converter/           # フォーマット変換
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
├── makefile
├── SPEC.md                  # 詳細仕様
├── PLAN.md                  # 実装計画
├── TODO.md                  # タスク一覧
└── KNOWLEDGE.md             # 設計判断の記録
```

## 前提条件

- Docker
- Discord Developer Portal でアプリケーション作成済み

## 環境構築

### Dev Container（推奨）

Go, Terraform, AWS CLI 等のバージョンが固定された開発コンテナを使用する。

VS Code の場合は「Reopen in Container」で自動起動。

docker compose の場合:

```bash
git clone <repository-url>
cd nemuri
make up
docker compose exec dev bash
```

`make up` はホストの UID/GID を自動検出してコンテナに渡すため、ファイル所有権の問題が発生しない。

初回起動時に `make setup`（Git hooks インストール + `go mod download`）が自動実行される。

### ツールバージョン

| ツール | バージョン |
|---|---|
| Go | 1.25.0 |
| Terraform | 1.14.6 |
| golangci-lint | 2.11.2 |
| tflint | 0.61.0 |
| AWS CLI | v2 |

Dev Container を使わない場合は上記を個別にインストールする。

### 手動セットアップ

#### 1. リポジトリのクローン

```bash
git clone <repository-url>
cd nemuri
```

#### 2. Go依存関係のインストール

```bash
go mod download
```

#### 3. Git hooks のセットアップ

コミット時にlint、プッシュ時にテストが自動実行される。

```bash
make setup-hooks
```

#### 4. 環境変数の設定

プロジェクトルートに `.env` ファイルを作成し、以下の変数を設定する。

```bash
ANTHROPIC_API_KEY=sk-ant-...
DISCORD_BOT_TOKEN=...
GITHUB_PAT=github_pat_...
```

#### 5. Terraformの初期化

```bash
terraform -chdir=terraform/envs/dev init
```

#### 6. インフラのコスト確認・プラン

```bash
make check
```

#### 7. デプロイ

Lambda のビルド、Docker イメージの ECR プッシュ、Terraform apply、シークレット登録をまとめて実行する。

```bash
make deploy
```

個別に実行する場合:

```bash
# Lambda関数のビルド
make build-lambda

# Dockerイメージのビルド・ECRプッシュ
make build-and-push-ecr

# Terraformの適用
make terraform-apply

# シークレットの登録 (.envから読み込み)
make put-secret
```

#### 8. Discordスラッシュコマンドの登録

```bash
./scripts/register_commands.sh
```

## 関連ドキュメント

| ファイル | 内容 |
|---|---|
| [SPEC.md](SPEC.md) | システム仕様・アーキテクチャ詳細 |
| [PLAN.md](PLAN.md) | フェーズ別実装計画 |
| [TODO.md](TODO.md) | タスク一覧・進捗管理 |
| [KNOWLEDGE.md](KNOWLEDGE.md) | 設計判断の背景と理由 |

## ライセンス

Private
