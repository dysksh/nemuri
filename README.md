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
- Discord Developer Portal でアプリケーション作成済み

## 環境構築

### Dev Container（推奨）

Go, Terraform, AWS CLI 等のバージョンが固定された開発コンテナを使用する。

VS Code の場合は「Reopen in Container」で自動起動。

docker compose の場合:

```bash
git clone <repository-url>
cd nemuri
make up        # UID/GID を自動検出してコンテナ起動
make dev       # コンテナ内にシェル接続
```

`make up` はホストの UID/GID を自動検出してコンテナに渡すため、ファイル所有権の問題が発生しない。

初回起動時に `make setup`（Git hooks インストール + `go mod download`）が自動実行される。

コンテナの停止:

```bash
make down
```

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

#### 1. リポジトリのクローンと初期設定

```bash
git clone <repository-url>
cd nemuri
make setup     # Git hooks インストール + go mod download
```

`make setup` は以下を実行する:
- `scripts/pre-commit` → `.git/hooks/pre-commit` にコピー（コミット時に lint を自動実行）
- `scripts/pre-push` → `.git/hooks/pre-push` にコピー（プッシュ時にテストを自動実行）
- `go mod download`

Git hooks だけ個別にインストールする場合は `make setup-hooks`。

#### 2. 環境変数の設定

プロジェクトルートに `.env` ファイルを作成し、以下の変数を設定する。`make put-secret` でこのファイルから AWS Secrets Manager に登録される。

```bash
ANTHROPIC_API_KEY=sk-ant-...
DISCORD_BOT_TOKEN=...
GITHUB_PAT=github_pat_...
```

#### 3. インフラのコスト確認・プラン

デプロイ前に必ずコストとプランを確認する:

```bash
make check     # infracost breakdown + terraform init + terraform plan
```

#### 4. デプロイ

Lambda ビルド、Docker イメージの ECR プッシュ、Terraform apply、シークレット登録をまとめて実行する。

```bash
make deploy
```

`make deploy` は以下を順に実行する:

| ステップ | make ターゲット | 内容 |
|---|---|---|
| 1 | `make build-lambda` | Lambda 関数のビルド（`scripts/build_lambda_ingress.sh`, `scripts/build_lambda_runner.sh`） |
| 2 | `make build-and-push-ecr` | Docker イメージのビルド・ECR プッシュ（`scripts/build_and_push.sh`） |
| 3 | `make terraform-apply` | `terraform init` + `terraform apply -auto-approve`（`terraform/envs/dev`） |
| 4 | `make put-secret` | `.env` から AWS Secrets Manager にシークレット登録 |

個別に実行することも可能。

#### 5. Discord スラッシュコマンドの登録

```bash
./scripts/register_commands.sh
```

### 開発時コマンド

| コマンド | 内容 |
|---|---|
| `make test` | 全パッケージのテスト実行（internal + lambda-ingress + lambda-runner） |
| `make lint` | gofmt + golangci-lint + terraform fmt + tflint |
| `make build-lambda` | Lambda 関数のビルド |
| `make build-and-push-ecr` | Docker イメージのビルド・ECR プッシュ |
| `make terraform-apply` | Terraform の適用 |
| `make put-secret` | `.env` のシークレットを Secrets Manager に登録 |
| `make deploy` | 上記4つをまとめて実行 |

## 関連ドキュメント

| ファイル | 内容 |
|---|---|
| [SPEC.md](SPEC.md) | システム仕様・アーキテクチャ詳細 |
| [PLAN.md](PLAN.md) | フェーズ別実装計画 |
| [TODO.md](TODO.md) | タスク一覧・進捗管理 |
| [KNOWLEDGE.md](KNOWLEDGE.md) | 設計判断の背景と理由 |

## ライセンス

Private
