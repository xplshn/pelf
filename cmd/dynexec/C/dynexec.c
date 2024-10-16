#include <libgen.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <dirent.h>
#include <regex.h>
#include <unistd.h>
#include <sys/stat.h>
#include <errno.h>

#define DYNEXE_NAME "dynexe"
extern char **environ;

char* match_linker_name(const char* shared_lib) {
    struct dirent *entry;
    DIR *dp;
    regex_t regex;
    int reti;
    static char linker_name[256];

    // Compile regex to match "ld-*-*.so.*"
    reti = regcomp(&regex, "^ld-.*-.*\\.so\\..*", 0);
    if (reti) {
        fprintf(stderr, "Could not compile regex\n");
        exit(EXIT_FAILURE);
    }

    dp = opendir(shared_lib);
    if (dp == NULL) {
        perror("opendir");
        exit(EXIT_FAILURE);
    }

    while ((entry = readdir(dp))) {
        struct stat st;
        char path[1024];
        snprintf(path, sizeof(path), "%s/%s", shared_lib, entry->d_name);
        if (stat(path, &st) == 0 && S_ISREG(st.st_mode)) {
            reti = regexec(&regex, entry->d_name, 0, NULL, 0);
            if (!reti) {
                snprintf(linker_name, sizeof(linker_name), "%s", entry->d_name);
                closedir(dp);
                regfree(&regex);
                return linker_name;
            }
        }
    }

    closedir(dp);
    regfree(&regex);
    return NULL;
}

char* realpath_alloc(const char* path) {
    char* resolved_path = realpath(path, NULL);
    if (!resolved_path) {
        perror("realpath");
        exit(EXIT_FAILURE);
    }
    return resolved_path;
}

char* custom_basename(const char* path) {
    char* base = strrchr(path, '/');
    return base ? base + 1 : (char*)path;
}

int is_file(const char* path) {
    struct stat path_stat;
    if (stat(path, &path_stat) != 0) {
        return 0;
    }
    return S_ISREG(path_stat.st_mode);
}

int main(int argc, char* argv[]) {
    char* dynexe = realpath_alloc("/proc/self/exe");
    char* dynexe_dir = strdup(dynexe);
    dynexe_dir = dirname(dynexe_dir);
    char lower_dir[512];

    snprintf(lower_dir, sizeof(lower_dir), "%s/../", dynexe_dir);

    // Check if we are in the "bin" directory
    if (strcmp(custom_basename(dynexe_dir), "bin") == 0 && is_file(lower_dir)) {
        free(dynexe_dir);
        dynexe_dir = realpath_alloc(lower_dir);
    }

    char* shared_bin = malloc(strlen(dynexe_dir) + 12);
    snprintf(shared_bin, strlen(dynexe_dir) + 12, "%s/shared/bin", dynexe_dir);

    char* shared_lib = malloc(strlen(dynexe_dir) + 12);
    snprintf(shared_lib, strlen(dynexe_dir) + 12, "%s/shared/lib", dynexe_dir);

    // Determine the binary to run
    char* bin_name = custom_basename(argv[0]);
    if (strcmp(bin_name, DYNEXE_NAME) == 0 && argc > 1) {
        bin_name = argv[1];
        argv++;
        argc--;
    }

    char* bin = malloc(strlen(shared_bin) + strlen(bin_name) + 2);
    snprintf(bin, strlen(shared_bin) + strlen(bin_name) + 2, "%s/%s", shared_bin, bin_name);

    char* linker_name = match_linker_name(shared_lib);
    if (!linker_name) {
        fprintf(stderr, "No valid linker found in %s\n", shared_lib);
        exit(EXIT_FAILURE);
    }

    char* linker = malloc(strlen(shared_lib) + strlen(linker_name) + 2);
    snprintf(linker, strlen(shared_lib) + strlen(linker_name) + 2, "%s/%s", shared_lib, linker_name);

    // Prepare arguments for execve
    char* exec_args[argc + 4];
    exec_args[0] = linker;
    exec_args[1] = "--library-path";
    exec_args[2] = shared_lib;
    exec_args[3] = bin;
    for (int i = 1; i < argc; i++) {
        exec_args[3 + i] = argv[i];
    }
    exec_args[argc + 3] = NULL;

    // Execute the binary using execve
    if (execve(linker, exec_args, environ) == -1) {
        fprintf(stderr, "Failed to execute %s: %s\n", linker, strerror(errno));
        exit(EXIT_FAILURE);
    }

    // Clean up
    free(dynexe);
    free(dynexe_dir);
    free(shared_bin);
    free(shared_lib);
    free(bin);
    free(linker);

    return 0;
}
