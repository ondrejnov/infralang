terraform {
  required_version = ">= 1.5.0"

  required_providers {
    null = {
      source                = "hashicorp/null"
      version               = "3.3.1"
      configuration_aliases = [null.east]
    }
  }
}
