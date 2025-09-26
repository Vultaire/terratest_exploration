resource "juju_model" "main" {
    name = "main"
}

resource "juju_application" "ubuntu" {
    name = "ubuntu"
    model = juju_model.main.name

    charm {
        name = "ubuntu"
        channel = "latest/stable"
        base = "ubuntu@22.04"
    }

    constraints = "tags=${var.tags},${var.physical_tags}"

    units = 1
}
