resource "null_resource" "main" {
  provider = null.east

  triggers = {
    Marker = var.marker
  }
}
