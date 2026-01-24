export namespace main {
	
	export class ProjectInfo {
	    path: string;
	    name: string;
	    score: number;
	    hasSession: boolean;
	    tool: string;
	    sessionId: string;
	
	    static createFrom(source: any = {}) {
	        return new ProjectInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.score = source["score"];
	        this.hasSession = source["hasSession"];
	        this.tool = source["tool"];
	        this.sessionId = source["sessionId"];
	    }
	}
	export class QuickLaunchFavorite {
	    name: string;
	    path: string;
	    tool: string;
	    shortcut?: string;
	
	    static createFrom(source: any = {}) {
	        return new QuickLaunchFavorite(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.tool = source["tool"];
	        this.shortcut = source["shortcut"];
	    }
	}
	export class SessionInfo {
	    id: string;
	    title: string;
	    projectPath: string;
	    groupPath: string;
	    tool: string;
	    status: string;
	    tmuxSession: string;
	    isRemote: boolean;
	    remoteHost?: string;
	    gitBranch?: string;
	    isWorktree?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.projectPath = source["projectPath"];
	        this.groupPath = source["groupPath"];
	        this.tool = source["tool"];
	        this.status = source["status"];
	        this.tmuxSession = source["tmuxSession"];
	        this.isRemote = source["isRemote"];
	        this.remoteHost = source["remoteHost"];
	        this.gitBranch = source["gitBranch"];
	        this.isWorktree = source["isWorktree"];
	    }
	}

}

