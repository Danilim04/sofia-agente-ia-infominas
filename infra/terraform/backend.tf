# ==============================================================================
# State — LOCAL por enquanto (POC, um único operador). O terraform.tfstate
# fica na máquina de quem roda o apply e está no .gitignore.
#
# Quando precisar de state compartilhado, o padrão da casa é Cloudflare R2
# (S3-compatível, free tier) com lockfile nativo — descomente e ajuste:
#
# terraform {
#   backend "s3" {
#     bucket = "azapfy-bot-tfstate"
#     key    = "bot/terraform.tfstate"
#     region = "auto"
#     endpoints = {
#       s3 = "https://<ACCOUNT_ID>.r2.cloudflarestorage.com"
#     }
#     use_lockfile                = true
#     skip_credentials_validation = true
#     skip_region_validation      = true
#     skip_requesting_account_id  = true
#     skip_metadata_api_check     = true
#     skip_s3_checksum            = true
#   }
# }
#
# Pré-requisitos: bucket criado no dashboard R2 + token "Object Read & Write"
# exportado como AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY. Depois:
#   terraform init -migrate-state
# ==============================================================================
