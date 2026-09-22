#include "my_application.h"

#include <dlfcn.h>
#include <stdio.h>

// Flutter's Linux embedder links libEGL at runtime. Without libEGL.so.1 the
// process aborts before any window appears; print an install hint instead.
static int require_libegl() {
  void* handle = dlopen("libEGL.so.1", RTLD_NOW | RTLD_GLOBAL);
  if (handle != nullptr) {
    dlclose(handle);
    return 0;
  }
  const char* err = dlerror();
  fprintf(stderr,
          "Monitor cannot start: missing libEGL.so.1%s%s%s\n"
          "Install it, then retry:\n"
          "  Debian/Ubuntu:  sudo apt install libegl1 libgtk-3-0\n"
          "  Fedora:         sudo dnf install mesa-libEGL gtk3\n",
          err && err[0] ? " (" : "", err && err[0] ? err : "",
          err && err[0] ? ")" : "");
  return 1;
}

int main(int argc, char** argv) {
  if (require_libegl() != 0) {
    return 1;
  }
  g_autoptr(MyApplication) app = my_application_new();
  return g_application_run(G_APPLICATION(app), argc, argv);
}
