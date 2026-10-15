variable "BUILD_PLATFORM" {
  default = "linux/amd64"
}

variable "BUILD_JOBS" {
  default = "2"
}

variable "TELEPORT_VERSION" {
  default = "18.10.0"
}

group "default" {
  targets = ["runtime", "lab"]
}

target "common" {
  context    = "."
  dockerfile = "build/Dockerfile"
  platforms  = [BUILD_PLATFORM]
  args = {
    BUILD_JOBS       = BUILD_JOBS
    TELEPORT_VERSION = TELEPORT_VERSION
  }
}

target "runtime" {
  inherits = ["common"]
  target   = "runtime"
  tags     = ["teleport-zitadel:${TELEPORT_VERSION}"]
}

target "lab" {
  inherits = ["common"]
  target   = "lab"
  tags     = ["teleport-zitadel-lab:${TELEPORT_VERSION}"]
}

target "verified" {
  inherits = ["common"]
  target   = "verified"
  tags     = ["teleport-zitadel-build-test:${TELEPORT_VERSION}"]
}

target "artifacts" {
  inherits = ["common"]
  target   = "artifacts"
  output   = ["type=local,dest=dist/${replace(BUILD_PLATFORM, "/", "-")}"]
}
