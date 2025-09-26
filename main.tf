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

    units = 1
}
